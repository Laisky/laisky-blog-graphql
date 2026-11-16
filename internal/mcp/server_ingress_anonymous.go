package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
)

// Anonymous access policy.
//
// A request without any credential may discover the server but never execute
// anything. The policy is enforced twice:
//
//   - at the transport (withBearerAuthentication → serveAnonymous), which
//     answers every anonymous non-discovery message with 401 and a Bearer
//     challenge before the SDK parses it, so MCP clients start their sign-in
//     flow instead of receiving a tool-level error; and
//   - at SDK dispatch (requireVerifiedToolCaller), which refuses every tool
//     handler registered through registerTool (also when a client runs it as
//     a task) and every session tool, for a context the bearer gate did not
//     verify, should a future transport path ever forward such a call. The
//     SDK does not wrap AddTaskTool handlers; the server registers none, and
//     TestToolDispatchRequiresVerifiedCallerForEveryTool fails if the
//     advertised catalog ever differs from registerTool's.
//
// Discovery is the only anonymous capability; tenant authorization (which
// files, memories or requests a key may touch) and billing authorization (a
// paid tool's one-api consume) still run inside the tools for verified callers.

// anonymousDiscoveryMethods are the JSON-RPC requests a caller without a
// credential may send: protocol negotiation, liveness and catalog listing.
// It is an allowlist, so a method added by a later protocol revision or SDK
// release stays closed to anonymous callers until it is reviewed. Everything
// else (tools/call, tasks/*, completion/complete, resources/read, prompts/get,
// logging/setLevel, subscriptions, client responses) needs a verified API key.
var anonymousDiscoveryMethods = map[mcp.MCPMethod]struct{}{
	mcp.MethodInitialize:             {},
	mcp.MethodServerDiscover:         {},
	mcp.MethodPing:                   {},
	mcp.MethodToolsList:              {},
	mcp.MethodPromptsList:            {},
	mcp.MethodResourcesList:          {},
	mcp.MethodResourcesTemplatesList: {},
}

// anonymousNotification is the only client notification an anonymous caller
// may send: the legacy handshake's notifications/initialized. Every other
// client notification acts on requests or state that discovery never
// creates, and the cancellation notification (mcp.MethodNotificationCancelled)
// is dangerous: the SDK keys in-flight requests without a session (all
// 2026-07-28 requests) by request id alone, so a bare cancellation could
// abort another caller's request.
const anonymousNotification = mcp.MethodNotificationInitialized

// maxLoggedMethodBytes bounds a caller-chosen method name in logs.
const maxLoggedMethodBytes = 64

// jsonRPCRouting holds the members the SDK routes one message by. It is
// decoded the way the SDK's Streamable HTTP transport decodes a body (one JSON
// object, encoding/json), so the policy and the SDK always agree on the method
// a body names, including for duplicate or case-folded keys.
type jsonRPCRouting struct {
	ID     json.RawMessage `json:"id"`
	Method mcp.MCPMethod   `json:"method"`
}

// classifyAnonymousMessage reports the method body names and whether an
// anonymous caller may send it. A body that is not exactly one JSON-RPC
// object, a batch, or a client response is never anonymous discovery: a body
// the policy cannot classify must not reach the SDK.
func classifyAnonymousMessage(body []byte) (mcp.MCPMethod, bool) {
	var routing jsonRPCRouting
	if err := json.Unmarshal(body, &routing); err != nil {
		return "", false
	}
	if _, ok := anonymousDiscoveryMethods[routing.Method]; ok {
		return routing.Method, true
	}
	return routing.Method, len(routing.ID) == 0 && routing.Method == anonymousNotification
}

// serveAnonymous applies the anonymous policy to a request without any
// credential. Its body budget shrinks to anonymousMaxBytes. A bodyless method
// (a legacy SSE GET stream, a DELETE) keeps reaching the SDK as before; a POST
// must carry one discovery request or client notification.
func serveAnonymous(w http.ResponseWriter, r *http.Request, next http.Handler, anonymousMaxBytes int64, metadataURL string, logger logSDK.Logger) {
	if err := httpbody.Tighten(w, r, anonymousMaxBytes); err != nil {
		writeRequestTooLarge(w, httpbody.Limit(err))
		return
	}
	if r.Method != http.MethodPost {
		next.ServeHTTP(w, r)
		return
	}

	body, err := bufferRequestBody(r)
	switch {
	case httpbody.IsTooLarge(err):
		writeRequestTooLarge(w, httpbody.Limit(err))
		return
	case err != nil:
		if logger != nil {
			logger.Debug("anonymous mcp request body unreadable", zap.Error(err))
		}
		writeUnreadableBody(w)
		return
	}

	method, allowed := classifyAnonymousMessage(body)
	if !allowed {
		if logger != nil {
			logger.Debug("anonymous mcp request refused: method requires an api key",
				zap.String("jsonrpc_method", boundedLogValue(string(method), maxLoggedMethodBytes)))
		}
		writeAuthenticationRequired(w, metadataURL)
		return
	}
	next.ServeHTTP(w, r)
}

// bufferRequestBody reads the whole body, already bounded in size and time by
// the transport budget, and replays it to the next handler.
func bufferRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	source := r.Body
	body, err := io.ReadAll(source)
	if err != nil {
		return nil, errors.Wrap(err, "read anonymous mcp request body")
	}
	r.Body = &loggingReplayBody{ReadCloser: source, prefix: bytes.NewReader(body)}
	return body, nil
}

// boundedLogValue truncates a caller-controlled value for logging.
func boundedLogValue(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// writeAuthenticationRequired answers a request that presented no credential
// for a method that needs one. RFC 6750 §3.1 omits the error code when the
// request had no authentication information, so the challenge carries only
// the RFC 9728 metadata pointer the MCP authorization spec asks for.
func writeAuthenticationRequired(w http.ResponseWriter, metadataURL string) {
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeIngressJSON(w, http.StatusUnauthorized, ingressError{
		Error:            "authentication_required",
		Description:      authenticationRequiredDescription,
		ResourceMetadata: metadataURL,
	})
}

// writeUnreadableBody answers 400 for an anonymous body that could not be
// read (for example a stalled upload cut by the read deadline) and closes the
// connection, whose framing may no longer be intact.
func writeUnreadableBody(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	writeIngressJSON(w, http.StatusBadRequest, ingressError{
		Error:       "invalid_request",
		Description: "The request body could not be read.",
	})
}

// requireVerifiedToolCaller is the dispatch-level half of the anonymous
// policy. The SDK runs it around every regular and session tool handler, so
// no tool, audit row or billing report runs for a context without a
// credential the bearer gate verified. The transport normally refuses such calls first with 401;
// reaching this check means a transport path forwarded an unverified call,
// which is logged at WARN.
func requireVerifiedToolCaller(next srv.ToolHandlerFunc) srv.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, verified := mcpauth.FromContext(ctx); verified {
			return next(ctx, request)
		}
		LoggerFromContext(ctx).Warn("refused mcp tool call without a verified api key",
			zap.String("tool", boundedLogValue(request.Params.Name, maxLoggedMethodBytes)))
		return mcp.NewToolResultError(authenticationRequiredDescription), nil
	}
}

// serveVerified forwards a request whose credential the gate verified, except
// a 2026-07-28 cancellation notification (mcp.MethodNotificationCancelled),
// which is accepted (202) and dropped.
//
// The 2026-07-28 Streamable HTTP transport defines no client-to-server
// notifications: a client cancels a request by closing its response stream,
// which the SDK already turns into a canceled request context. The SDK
// nevertheless still acts on a POSTed cancellation notification, keyed by the
// request id alone because modern requests carry no session, so it would
// abort whichever caller's in-flight request has that id. Legacy sessions keep
// their cancellations: their keys include the credential-bound session id.
func serveVerified(w http.ResponseWriter, r *http.Request, next http.Handler, logger logSDK.Logger) {
	if r.Method != http.MethodPost || !mcp.IsModernProtocol(r.Header.Get(mcp.HeaderProtocolVersion)) {
		next.ServeHTTP(w, r)
		return
	}

	// The whole body is classified (already bounded in size and time by the
	// transport budget): a method member placed after a large params value
	// must not escape the check.
	body, err := bufferRequestBody(r)
	switch {
	case httpbody.IsTooLarge(err):
		writeRequestTooLarge(w, httpbody.Limit(err))
		return
	case err != nil:
		if logger != nil {
			logger.Debug("mcp request body unreadable", zap.Error(err))
		}
		writeUnreadableBody(w)
		return
	}

	var routing jsonRPCRouting
	if json.Unmarshal(body, &routing) == nil && routing.Method == mcp.MethodNotificationCancelled {
		if logger != nil {
			logger.Debug("ignored a 2026-07-28 cancellation notification; modern requests are canceled by closing their stream",
				zap.String("jsonrpc_method", string(routing.Method)))
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	next.ServeHTTP(w, r)
}
