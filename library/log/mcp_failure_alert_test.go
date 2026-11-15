package log

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// forbiddenMCPMarshaler detects accidental evaluation of arbitrary alert payloads.
type forbiddenMCPMarshaler struct{}

// MarshalLogObject must never run at the MCP alert boundary.
func (forbiddenMCPMarshaler) MarshalLogObject(zapcore.ObjectEncoder) error {
	panic("unreviewed payload marshaler executed")
}

// TestMCPFailureAlertBoundary rejects payloads, forged envelopes and scope lookalikes.
func TestMCPFailureAlertBoundary(t *testing.T) {
	valid := MCPFailureDiagnosticField(MCPFailureSummary{Method: "tools/call", RequestID: 3, ErrorKind: "tool_not_found", ErrorCode: -32602})
	for _, tc := range []struct {
		name, scope, message string
		fields               []zapcore.Field
		useful               bool
	}{
		{"authored", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{valid}, true},
		{"missing", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", nil, false},
		{"forged string", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{zap.String("mcp_failure", "private-canary")}, false},
		{"forged object", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{zap.Object("mcp_failure", forbiddenMCPMarshaler{})}, false},
		{"duplicates", "graphql.laisky-graphql.mcp_hooks", "mcp request failed", []zapcore.Field{valid, valid}, false},
		{"scope lookalike", "graphql.laisky-graphql.mcp_hooks_private", "mcp request failed", []zapcore.Field{valid}, false},
		{"other event", "graphql.laisky-graphql.mcp_hooks", "unrelated event", []zapcore.Field{valid}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, messages := alertReceiver(t)
			alert := adoptionAlert(t, endpoint)
			logger := zap.New(WebDiagnosticAlertCore(alert)).Named(tc.scope).
				With(zap.String("private_bound", "private-bound-canary"), zap.Namespace("private_namespace"))
			fields := append([]zapcore.Field{}, tc.fields...)
			fields = append(fields, zap.String("request", "private-request-canary"), zap.Object("payload", forbiddenMCPMarshaler{}))
			logger.Error(tc.message, fields...)
			message := receivedAlert(t, messages)
			decoded := adoptionJSONFields(t, message)
			if tc.useful {
				require.Len(t, decoded, 1)
				require.Contains(t, decoded, "mcp_failure")
			} else {
				require.Empty(t, decoded)
			}
			require.NotContains(t, message, "canary")
			var projected []zapcore.Field
			hook := WebDiagnosticAlertHook(func(_ zapcore.Entry, values []zapcore.Field) error {
				projected = values
				return nil
			})
			require.NoError(t, hook(zapcore.Entry{LoggerName: tc.scope, Message: tc.message}, fields))
			if tc.useful {
				require.Len(t, projected, 1)
				require.Equal(t, "mcp_failure", projected[0].Key)
			} else {
				require.Empty(t, projected)
			}
			alert.Close()
			<-alert.Done()
			require.Empty(t, messages)
		})
	}
}

// TestMCPFailureDiagnosticNormalization preserves safe IDs without encoding arbitrary client values.
func TestMCPFailureDiagnosticNormalization(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   any
		want any
	}{
		{"integer", 3, json.Number("3")},
		{"int64", int64(3), json.Number("3")},
		{"float", float64(3), json.Number("3")},
		{"json number", json.Number("3"), json.Number("3")},
		{"uuid", "0199c118-4229-7000-8000-000000000001", "0199c118-4229-7000-8000-000000000001"},
		{"private string", "private-id-canary", nil},
		{"object", forbiddenMCPMarshaler{}, nil},
		{"fraction", 1.25, nil},
		{"nan", math.NaN(), nil},
		{"infinite", math.Inf(1), nil},
		{"large", int64(1 << 60), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := MCPFailureDiagnosticField(MCPFailureSummary{Method: "private-method-canary", RequestID: tc.id, ErrorKind: "private-error-canary", ErrorCode: 12345})
			encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{})
			field.AddTo(encoder)
			buffer, err := encoder.EncodeEntry(zapcore.Entry{}, nil)
			require.NoError(t, err)
			defer buffer.Free()
			decoded := adoptionJSONFields(t, buffer.String())["mcp_failure"].(map[string]any)
			require.Equal(t, "unknown", decoded["method"])
			require.Equal(t, "request_failed", decoded["error_kind"])
			require.Equal(t, tc.want, decoded["request_id"])
			require.NotContains(t, decoded, "error_code")
			_, err = uuid.Parse(decoded["failure_id"].(string))
			require.NoError(t, err)
			require.NotContains(t, buffer.String(), "canary")
		})
	}
}
