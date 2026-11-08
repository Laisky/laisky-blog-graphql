package mcp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
)

// Default MCP transport body budgets. The authenticated cap admits the largest
// legitimate tool call: extract_key_info materials default to 10 MB of text,
// whose JSON encoding plus envelope stays well below 32 MiB. Anonymous callers
// may only discover the server (initialize, tools/list, ping, notifications).
const (
	DefaultMCPMaxBodyBytes          int64 = 32 << 20
	DefaultMCPAnonymousMaxBodyBytes int64 = 1 << 20
	DefaultMCPBodyReadTimeout             = 60 * time.Second
)

// RequestBodyBudget bounds MCP transport request bodies.
type RequestBodyBudget struct {
	// MaxBytes caps a request that carries a validated credential.
	MaxBytes int64
	// AnonymousMaxBytes caps a request without any credential.
	AnonymousMaxBytes int64
	// ReadTimeout bounds the time spent reading one body.
	ReadTimeout time.Duration
}

// DefaultRequestBodyBudget returns the transport defaults.
func DefaultRequestBodyBudget() RequestBodyBudget {
	return RequestBodyBudget{
		MaxBytes:          DefaultMCPMaxBodyBytes,
		AnonymousMaxBytes: DefaultMCPAnonymousMaxBodyBytes,
		ReadTimeout:       DefaultMCPBodyReadTimeout,
	}
}

// normalized fills unset fields with defaults and keeps the anonymous cap within the authenticated one.
func (b RequestBodyBudget) normalized() RequestBodyBudget {
	defaults := DefaultRequestBodyBudget()
	if b.MaxBytes <= 0 {
		b.MaxBytes = defaults.MaxBytes
	}
	if b.AnonymousMaxBytes <= 0 {
		b.AnonymousMaxBytes = defaults.AnonymousMaxBytes
	}
	b.AnonymousMaxBytes = min(b.AnonymousMaxBytes, b.MaxBytes)
	if b.ReadTimeout <= 0 {
		b.ReadTimeout = defaults.ReadTimeout
	}
	return b
}

// WithAuthenticator makes the MCP transport validate bearers with authenticator
// instead of the process-wide installed one.
func WithAuthenticator(authenticator *mcpauth.Authenticator) ServerOption {
	return func(services *sharedServerServices) { services.authenticator = authenticator }
}

// WithRequestBodyBudget overrides the MCP transport body budget.
func WithRequestBodyBudget(budget RequestBodyBudget) ServerOption {
	return func(services *sharedServerServices) { services.bodyBudget = budget }
}

// withRequestBodyBudget is the outermost transport wrapper: it refuses a
// declared over-budget body before any byte is read, caps streamed bodies, and
// bounds the time spent reading them. It precedes the request logger because
// the logger reads a body preview.
func withRequestBodyBudget(next http.Handler, budget RequestBodyBudget, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	budget = budget.normalized()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := httpbody.Enforce(w, r, httpbody.Budget{MaxBytes: budget.MaxBytes, ReadTimeout: budget.ReadTimeout})
		if err != nil {
			writeRequestTooLarge(w, httpbody.Limit(err))
			return
		}
		if result.DeadlineErr != nil && logger != nil {
			logger.Debug("mcp body read deadline unavailable", zap.Error(result.DeadlineErr))
		}
		next.ServeHTTP(w, r)
	})
}

// withBearerAuthentication is the mandatory credential gate of the transport.
// A present bearer (header or legacy query key) must be validated by the
// credential authority before a session is issued or any tool runs; the
// verified context then serves the whole request, including nested mcp_pipe
// calls. A request without credentials keeps anonymous discovery but only
// within the anonymous body budget; tools still require authorization.
func withBearerAuthentication(next http.Handler, resolve func() *mcpauth.Authenticator, anonymousMaxBytes int64, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header, source := resolveRequestAuthorizationHeader(r)
		// A bearer scheme without a token (e.g. a client whose key variable is
		// empty) carries no credential: it keeps anonymous discovery, as before,
		// and can never yield a tenant.
		if library.StripBearerPrefix(header) == "" {
			if err := httpbody.Tighten(w, r, anonymousMaxBytes); err != nil {
				writeRequestTooLarge(w, httpbody.Limit(err))
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// The per-source validation budget is keyed on the client address.
		ctx := mcpauth.EnsureClientAddress(r.Context(), r.RemoteAddr)
		authCtx, err := resolve().Authenticate(ctx, header)
		if err != nil {
			if mcpauth.IsUnavailable(err) {
				if logger != nil {
					logger.Warn("mcp api key validation unavailable", zap.String("auth_source", source), zap.Error(err))
				}
				writeValidationUnavailable(w)
				return
			}
			if logger != nil {
				logger.Debug("mcp bearer rejected", zap.String("auth_source", source), zap.Error(err))
			}
			writeInvalidToken(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(mcpauth.WithContext(ctx, authCtx)))
	})
}

// invalidTokenDescription is the public reason of every bearer rejection.
const invalidTokenDescription = "The API key is not a valid, active Laisky MCP API key." //nolint:gosec // G101: an error message, not a credential

// ingressError is the JSON body of a transport-level rejection.
type ingressError struct {
	Error            string `json:"error"`
	Description      string `json:"error_description"`
	ResourceMetadata string `json:"resource_metadata,omitempty"`
	LimitBytes       int64  `json:"limit_bytes,omitempty"`
}

// writeInvalidToken answers an RFC 6750 invalid_token challenge with the
// RFC 9728 protected-resource metadata pointer the MCP authorization spec uses.
func writeInvalidToken(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="`+invalidTokenDescription+
		`", resource_metadata="`+mcpauth.ResourceMetadataURL+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeIngressJSON(w, http.StatusUnauthorized, ingressError{
		Error:            "invalid_token",
		Description:      invalidTokenDescription,
		ResourceMetadata: mcpauth.ResourceMetadataURL,
	})
}

// writeValidationUnavailable fails closed with a retryable status: the key is
// neither accepted nor declared invalid while the authority is unreachable.
func writeValidationUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(mcpauth.RetryAfterSeconds))
	w.Header().Set("Cache-Control", "no-store")
	writeIngressJSON(w, http.StatusServiceUnavailable, ingressError{
		Error:       "temporarily_unavailable",
		Description: "API key validation is temporarily unavailable; retry shortly.",
	})
}

// writeRequestTooLarge answers 413 and closes the connection instead of
// draining an oversized body.
func writeRequestTooLarge(w http.ResponseWriter, limit int64) {
	w.Header().Set("Connection", "close")
	writeIngressJSON(w, http.StatusRequestEntityTooLarge, ingressError{
		Error:       "request_too_large",
		Description: "The request body exceeds the MCP transport limit.",
		LimitBytes:  limit,
	})
}

// writeIngressJSON writes a small JSON error through the MCP MIME boundary.
func writeIngressJSON(w http.ResponseWriter, status int, payload ingressError) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"internal_error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	setMCPResponseHeaders(w.Header())
	w.WriteHeader(status)
	_, _ = writeMCPResponse(w, body)
}
