package auth

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
)

const (
	// RetryAfterSeconds is the Retry-After hint sent when validation is unavailable.
	RetryAfterSeconds = 5
	// ResourceMetadataURL is the RFC 9728 protected-resource metadata document
	// advertised in Bearer challenges.
	ResourceMetadataURL = "https://mcp.laisky.com/.well-known/oauth-protected-resource"
)

// HTTPMiddleware enforces authorization with the installed Authenticator and
// injects the verified auth context into request contexts.
func HTTPMiddleware(next http.Handler) http.Handler {
	return HTTPMiddlewareWith(nil, next)
}

// HTTPMiddlewareWith enforces authorization with authenticator, or with the
// installed Authenticator when it is nil. Rejected credentials receive the
// console's 401 JSON shape; an unavailable authority receives 503.
func HTTPMiddlewareWith(authenticator *Authenticator, next http.Handler) http.Handler {
	if next == nil {
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := authenticator
		if active == nil {
			active = Installed()
		}
		ctx := EnsureClientIP(r.Context(), r.RemoteAddr)
		authCtx, err := active.Authenticate(ctx, r.Header.Get("Authorization"))
		if err != nil {
			WriteHTTPAuthError(w, r, err)
			return
		}

		next.ServeHTTP(w, r.WithContext(WithContext(ctx, authCtx)))
	})
}

// unavailableLog samples the console's validation-unavailable WARN lines.
var unavailableLog = NewLogSampler(time.Minute)

// WriteHTTPAuthError writes the console JSON error for an Authenticate failure:
// 503 with Retry-After when no verdict was available, otherwise 401.
func WriteHTTPAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if IsUnavailable(err) {
		logger := gmw.GetLogger(r.Context())
		if emit, suppressed := unavailableLog.Allow(time.Now()); emit {
			logger.Warn("api key validation unavailable", zap.Int64("suppressed_since_last", suppressed), zap.Error(err))
		} else {
			logger.Debug("api key validation unavailable", zap.Error(err))
		}
		w.Header().Set("Retry-After", strconv.Itoa(RetryAfterSeconds))
		writeJSONError(w, http.StatusServiceUnavailable, ErrValidationUnavailable.Error())
		return
	}
	writeJSONError(w, http.StatusUnauthorized, PublicMessage(err))
}

// PublicMessage maps an Authenticate failure to a stable, non-sensitive message.
func PublicMessage(err error) string {
	switch {
	case errors.Is(err, ErrMissingAuthorization):
		return ErrMissingAuthorization.Error()
	case errors.Is(err, ErrCredentialRejected):
		return ErrCredentialRejected.Error()
	case errors.Is(err, ErrValidationUnavailable):
		return ErrValidationUnavailable.Error()
	default:
		return ErrInvalidAuthorization.Error()
	}
}

// writeJSONError writes the standardized {"error": message} body.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message}) //nolint:errchkjson // best-effort error response
}
