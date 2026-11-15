package log

import (
	"encoding/json"
	"math"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// The MCP failure alert boundary admits one event: MCPRequestFailedMessage logged by the MCP protocol-hook
// logger, whose full name is serviceLoggerName + "." + MCPHooksLoggerScope. Producers use these constants.
const (
	// MCPHooksLoggerScope is the child scope of the MCP server's protocol-hook logger.
	MCPHooksLoggerScope = "mcp_hooks"
	// MCPRequestFailedMessage is the protocol-hook event whose sealed mcp_failure envelope may be alerted.
	MCPRequestFailedMessage = "mcp request failed"

	mcpHooksLoggerName = serviceLoggerName + "." + MCPHooksLoggerScope
	mcpFailureFieldKey = "mcp_failure"
	// mcpMaxSafeRequestID is the largest integer every JSON consumer decodes exactly (2^53 - 1).
	mcpMaxSafeRequestID = 1<<53 - 1
)

// MCPFailureKind is the source-classified, content-free category of one failed MCP request. It is derived
// from typed Go causes reported by the SDK, never from error text, and is independent of the JSON-RPC wire
// code: for example tool_not_found travels as -32602 and unsupported as -32601.
type MCPFailureKind string

// Supported MCPFailureKind values. Any other value is exported as MCPFailureRequestFailed.
const (
	// MCPFailureToolNotFound means tools/call named a tool this server has not registered.
	MCPFailureToolNotFound MCPFailureKind = "tool_not_found"
	// MCPFailureResourceNotFound means the requested resource does not exist.
	MCPFailureResourceNotFound MCPFailureKind = "resource_not_found"
	// MCPFailurePromptNotFound means the requested prompt does not exist.
	MCPFailurePromptNotFound MCPFailureKind = "prompt_not_found"
	// MCPFailureUnsupported means the method needs a capability this server does not advertise.
	MCPFailureUnsupported MCPFailureKind = "unsupported"
	// MCPFailureUnparsableRequest means the SDK could not decode the request parameters.
	MCPFailureUnparsableRequest MCPFailureKind = "unparsable_request"
	// MCPFailureNotificationBlocked means a server-to-client notification queue was full.
	MCPFailureNotificationBlocked MCPFailureKind = "notification_channel_blocked"
	// MCPFailureCanceled means the request context was canceled.
	MCPFailureCanceled MCPFailureKind = "canceled"
	// MCPFailureDeadlineExceeded means the request context deadline expired.
	MCPFailureDeadlineExceeded MCPFailureKind = "deadline_exceeded"
	// MCPFailureProtocolError is any other SDK protocol error that carries a JSON-RPC code.
	MCPFailureProtocolError MCPFailureKind = "protocol_error"
	// MCPFailureRequestFailed means no typed cause was recognized.
	MCPFailureRequestFailed MCPFailureKind = "request_failed"
)

// mcpNotificationFailureMethod is the pseudo-method the SDK reports for server-side notification delivery failures.
const mcpNotificationFailureMethod mcpgo.MCPMethod = "notification"

// mcpFailureMethods holds every method value the pinned SDK passes to its OnError hook: each request
// method HandleMessage dispatches plus the notification pseudo-method. TestMCPFailureMethodsMatchSDK
// derives the same set from the SDK source and fails when an upgrade adds or removes one.
var mcpFailureMethods = map[mcpgo.MCPMethod]struct{}{
	mcpgo.MethodInitialize:             {},
	mcpgo.MethodPing:                   {},
	mcpgo.MethodServerDiscover:         {},
	mcpgo.MethodSubscriptionsListen:    {},
	mcpgo.MethodSetLogLevel:            {},
	mcpgo.MethodResourcesList:          {},
	mcpgo.MethodResourcesTemplatesList: {},
	mcpgo.MethodResourcesRead:          {},
	mcpgo.MethodResourcesSubscribe:     {},
	mcpgo.MethodResourcesUnsubscribe:   {},
	mcpgo.MethodPromptsList:            {},
	mcpgo.MethodPromptsGet:             {},
	mcpgo.MethodToolsList:              {},
	mcpgo.MethodToolsCall:              {},
	mcpgo.MethodTasksGet:               {},
	mcpgo.MethodTasksList:              {},
	mcpgo.MethodTasksResult:            {},
	mcpgo.MethodTasksCancel:            {},
	mcpgo.MethodCompletionComplete:     {},
	mcpNotificationFailureMethod:       {},
}

// mcpFailureKinds lists the MCPFailureKind values the envelope exports verbatim.
var mcpFailureKinds = map[MCPFailureKind]struct{}{
	MCPFailureToolNotFound: {}, MCPFailureResourceNotFound: {}, MCPFailurePromptNotFound: {}, MCPFailureUnsupported: {},
	MCPFailureUnparsableRequest: {}, MCPFailureNotificationBlocked: {}, MCPFailureCanceled: {}, MCPFailureDeadlineExceeded: {},
	MCPFailureProtocolError: {}, MCPFailureRequestFailed: {},
}

// MCPFailureSummary is the source input for one failed MCP request: Method and RequestID as the SDK
// hook received them, plus ErrorKind and ErrorCode from typed causes. It must never carry raw errors
// or request payloads; values outside the supported sets are dropped when the envelope is built.
type MCPFailureSummary struct {
	Method    mcpgo.MCPMethod
	RequestID any
	ErrorKind MCPFailureKind
	ErrorCode int
}

// mcpFailureDiagnostic is an immutable, sealed alert projection authored at the source.
type mcpFailureDiagnostic struct {
	failureID string
	method    mcpgo.MCPMethod
	requestID any
	errorKind MCPFailureKind
	errorCode int
}

// MCPFailureDiagnosticField snapshots normalized diagnostics for one failed request as the mcp_failure
// object. Only this sealed value can cross the MCP alert boundary; arbitrary fields and marshalers cannot.
// Its public contract is:
//
//   - failure_id: a fresh server-generated UUID, identical in the alert and the complete local/central event.
//   - method: an SDK-reported method from mcpFailureMethods, otherwise "unknown".
//   - request_id: an integral JSON-RPC ID within ±(2^53-1) (int, int64, integral float64, json.Number or an
//     SDK RequestId holding one) or a canonical non-nil UUID string; any other client ID is omitted.
//   - error_kind: the source classification (MCPFailureKind), otherwise request_failed.
//   - error_code: the JSON-RPC wire code the SDK returned, only within the reserved -32768..-32000 range.
func MCPFailureDiagnosticField(summary MCPFailureSummary) zapcore.Field {
	diagnostic := mcpFailureDiagnostic{failureID: uuid.NewString(), method: "unknown", errorKind: MCPFailureRequestFailed}
	if _, ok := mcpFailureMethods[summary.Method]; ok {
		diagnostic.method = summary.Method
	}
	if _, ok := mcpFailureKinds[summary.ErrorKind]; ok {
		diagnostic.errorKind = summary.ErrorKind
	}
	if summary.ErrorCode >= -32768 && summary.ErrorCode <= -32000 {
		diagnostic.errorCode = summary.ErrorCode
	}
	diagnostic.requestID = safeMCPRequestID(summary.RequestID)
	return zap.Object(mcpFailureFieldKey, diagnostic)
}

// safeMCPRequestID returns an int64 or canonical UUID string for supported IDs, otherwise nil.
func safeMCPRequestID(requestID any) any {
	if wrapped, ok := requestID.(mcpgo.RequestId); ok {
		requestID = wrapped.Value()
	}
	switch id := requestID.(type) {
	case int:
		return safeMCPNumericID(int64(id))
	case int64:
		return safeMCPNumericID(id)
	case float64:
		if !math.IsNaN(id) && !math.IsInf(id, 0) && math.Trunc(id) == id && math.Abs(id) <= mcpMaxSafeRequestID {
			return int64(id)
		}
	case json.Number:
		if value, err := id.Int64(); err == nil {
			return safeMCPNumericID(value)
		}
	case string:
		if canonicalCorrelationID(id) {
			return id
		}
	}
	return nil
}

func safeMCPNumericID(id int64) any {
	if id >= -mcpMaxSafeRequestID && id <= mcpMaxSafeRequestID {
		return id
	}
	return nil
}

// MarshalLogObject emits only the fixed source-authored diagnostic shape.
func (diagnostic mcpFailureDiagnostic) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("failure_id", diagnostic.failureID)
	encoder.AddString("method", string(diagnostic.method))
	encoder.AddString("error_kind", string(diagnostic.errorKind))
	if diagnostic.errorCode != 0 {
		encoder.AddInt("error_code", diagnostic.errorCode)
	}
	switch id := diagnostic.requestID.(type) {
	case int64:
		encoder.AddInt64("request_id", id)
	case string:
		encoder.AddString("request_id", id)
	}
	return nil
}

// mcpFailureAlertFields selects the single sealed envelope of an exact MCP request-failure event.
// Any other scope or message, a forged value, or a duplicate envelope yields metadata-only output.
func mcpFailureAlertFields(entry zapcore.Entry, fields []zapcore.Field) []zapcore.Field {
	if entry.LoggerName != mcpHooksLoggerName || entry.Message != MCPRequestFailedMessage {
		return nil
	}
	var selected []zapcore.Field
	for _, field := range fields {
		if field.Key != mcpFailureFieldKey {
			continue
		}
		if _, ok := field.Interface.(mcpFailureDiagnostic); !ok || field.Type != zapcore.ObjectMarshalerType || selected != nil {
			return nil
		}
		selected = []zapcore.Field{field}
	}
	return selected
}
