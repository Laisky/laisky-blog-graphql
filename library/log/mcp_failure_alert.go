package log

import (
	"encoding/json"
	"math"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
)

const (
	mcpFailureCanceled = "canceled"
	mcpFailureDeadline = "deadline_exceeded"
)

// MCPFailureSummary describes content-free MCP diagnostics, never raw errors or request payloads.
type MCPFailureSummary struct {
	Method    string
	RequestID any
	ErrorKind string
	ErrorCode int
}

// mcpFailureDiagnostic is an immutable, sealed alert projection authored at the source.
type mcpFailureDiagnostic struct {
	failureID string
	method    string
	requestID any
	errorKind string
	errorCode int
}

// MCPFailureDiagnosticField snapshots normalized diagnostics for one failed request.
// Only this sealed value can cross the MCP alert boundary; arbitrary fields and marshalers cannot.
// A server-generated failure ID joins alerts to the complete local/central event even when a client ID is unsafe.
func MCPFailureDiagnosticField(summary MCPFailureSummary) zapcore.Field {
	diagnostic := mcpFailureDiagnostic{failureID: uuid.NewString(), method: "unknown", errorKind: "request_failed"}
	switch summary.Method {
	case "initialize", "ping", "server/discover", "tools/list", "tools/call", "resources/list", "resources/templates/list", "resources/read",
		"resources/subscribe", "resources/unsubscribe", "prompts/list", "prompts/get", "logging/setLevel", "completion/complete", "subscriptions/listen":
		diagnostic.method = summary.Method
	}
	switch summary.ErrorKind {
	case "tool_not_found", "resource_not_found", "prompt_not_found", mcpFailureCanceled, mcpFailureDeadline, "protocol_error":
		diagnostic.errorKind = summary.ErrorKind
	}
	if summary.ErrorCode >= -32768 && summary.ErrorCode <= -32000 {
		diagnostic.errorCode = summary.ErrorCode
	}
	switch id := summary.RequestID.(type) {
	case int:
		diagnostic.requestID = safeMCPNumericID(int64(id))
	case int64:
		diagnostic.requestID = safeMCPNumericID(id)
	case float64:
		if !math.IsNaN(id) && !math.IsInf(id, 0) && math.Trunc(id) == id && math.Abs(id) <= 1<<53-1 {
			diagnostic.requestID = int64(id)
		}
	case json.Number:
		if value, err := id.Int64(); err == nil {
			diagnostic.requestID = safeMCPNumericID(value)
		}
	case string:
		if canonicalCorrelationID(id) {
			diagnostic.requestID = id
		}
	}
	return zap.Object("mcp_failure", diagnostic)
}

func safeMCPNumericID(id int64) any {
	if id >= -(1<<53-1) && id <= 1<<53-1 {
		return id
	}
	return nil
}

// MarshalLogObject emits only the fixed source-authored diagnostic shape.
func (diagnostic mcpFailureDiagnostic) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("failure_id", diagnostic.failureID)
	encoder.AddString("method", diagnostic.method)
	encoder.AddString("error_kind", diagnostic.errorKind)
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

func mcpFailureAlertFields(entry zapcore.Entry, fields []zapcore.Field) []zapcore.Field {
	if entry.LoggerName != "graphql.laisky-graphql.mcp_hooks" || entry.Message != "mcp request failed" {
		return nil
	}
	var selected []zapcore.Field
	for _, field := range fields {
		if field.Key != "mcp_failure" {
			continue
		}
		if _, ok := field.Interface.(mcpFailureDiagnostic); !ok || field.Type != zapcore.ObjectMarshalerType || selected != nil {
			return nil
		}
		selected = []zapcore.Field{field}
	}
	return selected
}
