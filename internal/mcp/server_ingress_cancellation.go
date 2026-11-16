package mcp

import (
	"encoding/json"
	"net/http"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// Modern cancellation policy.
//
// The 2026-07-28 Streamable HTTP transport defines no client-to-server
// notifications: a client cancels a request by closing its response stream,
// which the SDK already turns into a canceled request context. The mcp-go
// SDK (v1.1.1) nevertheless still acts on a POSTed cancellation notification
// (mcp.MethodNotificationCancelled), keyed by the request id alone because
// modern requests carry no session, so it would abort whichever caller's
// in-flight request has that id. The SDK
// dispatches notifications before it validates the Mcp-Method header against
// the body, so the header cannot be trusted for this and the body must be
// classified. Legacy sessions keep their cancellations: their keys include
// the credential-bound session id.
//
// The policy is a pure function of the request's protocol header and its
// whole body, so it is tested on its own (ingress_cancellation_test.go) and
// serveVerified only applies it.

// needsCancellationClassification reports whether r is a POST of the
// 2026-07-28 protocol revision, the only requests whose body the policy must
// classify before the SDK sees it. Everything else reaches the SDK unchanged.
func needsCancellationClassification(r *http.Request) bool {
	return r.Method == http.MethodPost && mcp.IsModernProtocol(r.Header.Get(mcp.HeaderProtocolVersion))
}

// isModernCancellation reports whether body, the whole body of a request
// for which needsCancellationClassification holds, is a cancellation
// notification the transport must drop. It decodes the body the way the
// SDK's Streamable HTTP transport does (one JSON object, encoding/json into
// jsonRPCRouting), so both agree on the method a body names: case-folded keys
// ("METHOD") and escaped ones match, and of duplicate keys the last one wins.
// A method placed after a large params value is found because the whole body
// is decoded. A body that does not decode (invalid JSON, a batch array, a
// non-string method) is not a cancellation; the SDK refuses it with a parse
// error of its own.
func isModernCancellation(body []byte) bool {
	var routing jsonRPCRouting
	return json.Unmarshal(body, &routing) == nil && routing.Method == mcp.MethodNotificationCancelled
}

// dropModernCancellation answers a dropped modern cancellation with the 202
// a Streamable HTTP server sends for an accepted notification.
func dropModernCancellation(w http.ResponseWriter, logger logSDK.Logger) {
	if logger != nil {
		logger.Debug("ignored a 2026-07-28 cancellation notification; modern requests are canceled by closing their stream",
			zap.String("jsonrpc_method", string(mcp.MethodNotificationCancelled)))
	}
	w.WriteHeader(http.StatusAccepted)
}

// serveVerified forwards a request whose credential the gate verified,
// applying the modern cancellation policy: a 2026-07-28 POST is buffered
// whole (already bounded in size, time and in-flight bytes by the transport
// budget) and dropped with 202 when it is a cancellation notification.
func serveVerified(w http.ResponseWriter, r *http.Request, next http.Handler, logger logSDK.Logger) {
	if !needsCancellationClassification(r) {
		next.ServeHTTP(w, r)
		return
	}
	body, ok := readBufferedBody(w, r, logger, "mcp request body unreadable")
	if !ok {
		return
	}
	if isModernCancellation(body) {
		dropModernCancellation(w, logger)
		return
	}
	next.ServeHTTP(w, r)
}
