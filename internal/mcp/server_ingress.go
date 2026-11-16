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
//
// Body reads follow a minimum data rate (like Kestrel's MinRequestBodyDataRate):
// after a grace period the client must keep delivering at least the minimum
// rate. Bytes delivered faster bank at most DefaultBodyMaxIdle of read time,
// so a burst cannot fund a later stall, and the whole read is capped
// absolutely.
//
// The in-flight budgets bound the body bytes in-flight MCP requests hold
// together, one for verified and one for anonymous callers
// (DefaultMCPInflightBodyFactor and DefaultMCPAnonymousInflightBodyFactor
// times the respective per-request cap unless configured): the per-request
// cap alone does not stop many concurrent requests from each buffering a
// large body. See server_ingress_inflight.go.
const (
	DefaultMCPMaxBodyBytes                int64 = 32 << 20
	DefaultMCPAnonymousMaxBodyBytes       int64 = 1 << 20
	DefaultMCPInflightBodyFactor          int64 = 4
	DefaultMCPAnonymousInflightBodyFactor int64 = 8
	DefaultBodyMinBytesPerSecond                = 4096
	DefaultBodyReadGrace                        = 10 * time.Second
	DefaultBodyMaxIdle                          = 30 * time.Second
	DefaultBodyMaxReadDuration                  = 30 * time.Minute
)

// RequestBodyBudget bounds MCP transport request bodies.
type RequestBodyBudget struct {
	// MaxBytes caps a request that carries a validated credential.
	MaxBytes int64
	// AnonymousMaxBytes caps a request without any credential.
	AnonymousMaxBytes int64
	// MaxInflightBytes bounds the body bytes held by all in-flight verified
	// requests together (httpbody.Inflight); one API key may hold at most a
	// quarter of it, and never less than MaxBytes. Unset uses
	// DefaultMCPInflightBodyFactor × MaxBytes; it is never below MaxBytes, so
	// one body at the cap always fits an idle transport.
	MaxInflightBytes int64
	// AnonymousMaxInflightBytes bounds the body bytes held by all in-flight
	// requests without a credential together. Unset uses
	// DefaultMCPAnonymousInflightBodyFactor × AnonymousMaxBytes; it is never
	// below AnonymousMaxBytes.
	AnonymousMaxInflightBytes int64
	// MinBytesPerSecond is the delivery rate a body must sustain after ReadGrace.
	MinBytesPerSecond float64
	// ReadGrace is the time allowed before the first bytes must arrive.
	ReadGrace time.Duration
	// MaxIdle is the longest pause a body that kept up with the minimum rate
	// may take; it bounds the read time a burst can bank (never below ReadGrace).
	MaxIdle time.Duration
	// MaxReadDuration caps one whole body read.
	MaxReadDuration time.Duration
}

// DefaultRequestBodyBudget returns the transport defaults.
func DefaultRequestBodyBudget() RequestBodyBudget {
	return RequestBodyBudget{
		MaxBytes:                  DefaultMCPMaxBodyBytes,
		AnonymousMaxBytes:         DefaultMCPAnonymousMaxBodyBytes,
		MaxInflightBytes:          DefaultMCPInflightBodyFactor * DefaultMCPMaxBodyBytes,
		AnonymousMaxInflightBytes: DefaultMCPAnonymousInflightBodyFactor * DefaultMCPAnonymousMaxBodyBytes,
		MinBytesPerSecond:         DefaultBodyMinBytesPerSecond,
		ReadGrace:                 DefaultBodyReadGrace,
		MaxIdle:                   DefaultBodyMaxIdle,
		MaxReadDuration:           DefaultBodyMaxReadDuration,
	}
}

// httpbody returns the transport budget in the shared mechanism's terms.
func (b RequestBodyBudget) httpbody() httpbody.Budget {
	return httpbody.Budget{MaxBytes: b.MaxBytes, MinBytesPerSecond: b.MinBytesPerSecond, Grace: b.ReadGrace, MaxIdle: b.MaxIdle, MaxDuration: b.MaxReadDuration}
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
	if b.MaxInflightBytes <= 0 {
		b.MaxInflightBytes = saturatingMultiple(b.MaxBytes, DefaultMCPInflightBodyFactor)
	}
	b.MaxInflightBytes = max(b.MaxInflightBytes, b.MaxBytes)
	if b.AnonymousMaxInflightBytes <= 0 {
		b.AnonymousMaxInflightBytes = saturatingMultiple(b.AnonymousMaxBytes, DefaultMCPAnonymousInflightBodyFactor)
	}
	b.AnonymousMaxInflightBytes = max(b.AnonymousMaxInflightBytes, b.AnonymousMaxBytes)
	if b.MinBytesPerSecond <= 0 {
		b.MinBytesPerSecond = defaults.MinBytesPerSecond
	}
	if b.ReadGrace <= 0 {
		b.ReadGrace = defaults.ReadGrace
	}
	if b.MaxIdle <= 0 {
		b.MaxIdle = defaults.MaxIdle
	}
	if b.MaxReadDuration <= 0 {
		b.MaxReadDuration = defaults.MaxReadDuration
	}
	return b
}

// WithAuthenticator makes the MCP transport validate bearers with authenticator
// instead of the process-wide installed one.
func WithAuthenticator(authenticator *mcpauth.Authenticator) ServerOption {
	return func(services *sharedServerServices) { services.authenticator = authenticator }
}

// WithResourceMetadataURL sets the RFC 9728 protected-resource metadata URL
// the transport advertises in Bearer challenges. Production derives it from
// the configured public origin (mcpauth.LoadPublicOrigin); empty keeps
// mcpauth.DefaultResourceMetadataURL.
func WithResourceMetadataURL(metadataURL string) ServerOption {
	return func(services *sharedServerServices) { services.resourceMetadataURL = metadataURL }
}

// WithRequestBodyBudget overrides the MCP transport body budget.
func WithRequestBodyBudget(budget RequestBodyBudget) ServerOption {
	return func(services *sharedServerServices) { services.bodyBudget = budget }
}

// bodyDeadlineEntryMCP labels the MCP transport in
// http_request_body_read_deadline_unarmed_total.
const bodyDeadlineEntryMCP = "mcp"

// withRequestBodyBudget is the outermost transport wrapper: it refuses a
// declared over-budget body before any byte is read, caps streamed bodies, and
// bounds the time spent reading them. It precedes the request logger because
// the logger reads a body preview.
//
// It also tracks every body for the in-flight budgets (httpbody.Track),
// because every POST body is held in memory: the MCP SDK io.ReadAll()s each
// one, and serveVerified and serveAnonymous buffer theirs for classification
// first. This wrapper runs before authentication, so it charges nothing yet:
// at most maxPendingBodyBytes (the logger's preview) may be read before the
// credential gate assigns the body to the verified budget, under its API key,
// or to the anonymous one (assignRequestBody). Slow senders and callers
// without a valid key therefore cannot spend the budget verified callers
// use. Every assigned body is charged the bytes it actually delivers, and its
// charge is released when the request's handler returns.
func withRequestBodyBudget(next http.Handler, budget RequestBodyBudget, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	budget = budget.normalized()
	unarmed := httpbody.NewUnarmedReporter(httpbody.UnarmedWarnInterval)
	budgets := newIngressBodyBudgets(budget)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := httpbody.Enforce(w, r, budget.httpbody())
		if err != nil {
			writeRequestTooLarge(w, httpbody.Limit(err))
			return
		}
		r, charge := httpbody.Track(budgets.attach(r), maxPendingBodyBytes)
		defer charge.Release()
		if result.DeadlineErr != nil {
			// Only the byte cap bounds this body now: count it and say so.
			warn, suppressed := unarmed.Record(bodyDeadlineEntryMCP, time.Now())
			switch {
			case logger == nil:
			case warn:
				logger.Warn("mcp body read deadline unavailable; only the byte cap bounds this body",
					zap.Int64("suppressed_since_last", suppressed), zap.Error(result.DeadlineErr))
			default:
				logger.Debug("mcp body read deadline unavailable", zap.Error(result.DeadlineErr))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withBearerAuthentication is the mandatory credential gate of the transport.
// A present bearer (header or legacy query key) must be validated by the
// credential authority before a session is issued or any tool runs; the
// verified context then serves the whole request, including nested mcp_pipe
// calls. A request without credentials may only discover the server, within
// the anonymous body budget (serveAnonymous); every other method is answered
// 401 before the SDK dispatches. Challenges point to metadataURL (RFC 9728);
// empty uses the default.
func withBearerAuthentication(next http.Handler, resolve func() *mcpauth.Authenticator, anonymousMaxBytes int64, metadataURL string, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	if metadataURL == "" {
		metadataURL = mcpauth.DefaultResourceMetadataURL
	}
	unavailableLog := mcpauth.NewUnavailableLog(time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header, source := resolveRequestAuthorizationHeader(r)
		// Bound the raw header before any parsing: splitting a 1 MiB header
		// (Go's default MaxHeaderBytes) of short fields allocates about nine
		// times its size, and mcpauth refuses such a header anyway.
		if len(header) > maxAuthorizationHeaderBytes {
			if logger != nil {
				logger.Debug("mcp bearer rejected: authorization header too long",
					zap.String("auth_source", source), zap.Int("header_bytes", len(header)))
			}
			writeInvalidToken(w, metadataURL)
			return
		}
		// A bearer scheme without a token (e.g. a client whose key variable is
		// empty) carries no credential: it keeps anonymous discovery, as before,
		// and can never yield a tenant.
		if library.StripBearerPrefix(header) == "" {
			if err := assignRequestBody(r, false, ""); err != nil {
				logInflightRefusal(logger, "anonymous", err)
				writeTransportBusy(w)
				return
			}
			serveAnonymous(w, r, next, anonymousMaxBytes, metadataURL, logger)
			return
		}

		// The per-source validation budget is keyed on the client identity
		// resolved by the request-identity middleware (clientip), or the peer.
		ctx := mcpauth.EnsureClientIP(r.Context(), r.RemoteAddr)
		authCtx, err := resolve().Authenticate(ctx, header)
		if err != nil {
			if mcpauth.IsUnavailable(err) {
				// A flood must yield one summary per cause and interval, not a
				// WARN per request; "local_budget" marks spent capacity.
				unavailableLog.Log(logger, "mcp api key validation unavailable", err, zap.String("auth_source", source))
				writeValidationUnavailable(w)
				return
			}
			if logger != nil {
				logger.Debug("mcp bearer rejected", zap.String("auth_source", source), zap.Error(err))
			}
			writeInvalidToken(w, metadataURL)
			return
		}
		// Only now, with the key verified, may the body draw on the verified
		// budget, within that key's share of it.
		if err := assignRequestBody(r, true, authCtx.APIKeyHash); err != nil {
			logInflightRefusal(logger, "verified", err)
			writeTransportBusy(w)
			return
		}
		serveVerified(w, r.WithContext(mcpauth.WithContext(ctx, authCtx)), next, logger)
	})
}

// authenticationRequiredDescription is the public reason of a refusal for a
// request that presented no credential.
const authenticationRequiredDescription = "This MCP method requires a valid Laisky MCP API key in the Authorization header."

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
func writeInvalidToken(w http.ResponseWriter, metadataURL string) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="`+invalidTokenDescription+
		`", resource_metadata="`+metadataURL+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeIngressJSON(w, http.StatusUnauthorized, ingressError{
		Error:            "invalid_token",
		Description:      invalidTokenDescription,
		ResourceMetadata: metadataURL,
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

// transportBusyRetryAfterSeconds is the Retry-After hint of a request refused
// because the in-flight body budget was spent; held bodies drain quickly.
const transportBusyRetryAfterSeconds = 2

// writeTransportBusy answers 503 for a body the transport's in-flight budget
// could not hold while it was read, and closes the connection instead of
// draining the rest of the body.
func writeTransportBusy(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Retry-After", strconv.Itoa(transportBusyRetryAfterSeconds))
	w.Header().Set("Cache-Control", "no-store")
	writeIngressJSON(w, http.StatusServiceUnavailable, ingressError{
		Error:       "temporarily_unavailable",
		Description: "The MCP transport is holding too many request bodies right now; retry shortly.",
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
