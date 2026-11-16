package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// productionMCPHooksLoggerName is the literal full scope production assigns, kept independent of the constants on purpose.
const productionMCPHooksLoggerName = "graphql.laisky-graphql.mcp_hooks"

// newMCPAlertFixture starts a synthetic alert receiver and an SDK sender that are stopped with the test.
func newMCPAlertFixture(t *testing.T) (*logSDK.Alert, <-chan string) {
	t.Helper()
	messages := make(chan string, 8)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body struct {
			Variables struct {
				Message string `json:"msg"`
			} `json:"variables"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil {
			http.Error(w, "invalid fixture", http.StatusBadRequest)
			return
		}
		messages <- body.Variables.Message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"fixture"}}}`))
	}))
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(context.Background())
	alert, err := logSDK.NewAlert(ctx, endpoint.URL, logSDK.WithAlertType("fixture"), logSDK.WithAlertToken("synthetic-token"))
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		alert.Close()
		<-alert.Done()
	})
	return alert, messages
}

// newMCPAlertLogger tees complete local JSON output with the production alert core under one logger name.
func newMCPAlertLogger(t *testing.T, name string, alert *logSDK.Alert, local *bytes.Buffer) logSDK.Logger {
	t.Helper()
	logger, err := logSDK.New(logSDK.WithName(name), logSDK.WithLevel(logSDK.LevelDebug), logSDK.WithOutputPaths([]string{}),
		logSDK.WithZapOptions(zap.WrapCore(func(zapcore.Core) zapcore.Core {
			return zapcore.NewTee(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(local), zap.DebugLevel),
				appLog.WebDiagnosticAlertCore(alert))
		})))
	require.NoError(t, err)
	return logger
}

// nextMCPAlert waits for the next delivered alert message.
func nextMCPAlert(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic MCP alert was not delivered")
		return ""
	}
}

// mcpAlertEnvelope decodes the mcp_failure object from an alert's trailing JSON fields, or nil.
func mcpAlertEnvelope(t *testing.T, message string) map[string]any {
	t.Helper()
	offset := strings.LastIndex(message, "\n{")
	require.GreaterOrEqual(t, offset, 0, message)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(message[offset+1:]), &fields))
	diagnostic, _ := fields["mcp_failure"].(map[string]any)
	return diagnostic
}

// mcpAlertsBefore queues a barrier behind every alert admitted so far and returns the alerts delivered
// before it. The SDK sender is one FIFO worker, so an empty result proves the preceding synchronous
// writes queued nothing more; Close cancels without flushing and could hide a duplicate.
func mcpAlertsBefore(t *testing.T, alert *logSDK.Alert, messages <-chan string) []string {
	t.Helper()
	barrier := "alert-barrier-" + uuid.NewString()
	require.NoError(t, alert.Send(barrier))
	var extra []string
	for {
		message := nextMCPAlert(t, messages)
		if strings.Contains(message, barrier) {
			return extra
		}
		extra = append(extra, message)
	}
}

// TestMCPRequestFailureAlert exercises the real dependency error hook and alert HTTP serializer.
func TestMCPRequestFailureAlert(t *testing.T) {
	ctx := context.Background()
	alert, messages := newMCPAlertFixture(t)
	var local bytes.Buffer
	logger := newMCPAlertLogger(t, productionMCPHooksLoggerName, alert, &local).With(zap.String("private_bound", "private-bound-canary"))
	server := srv.NewMCPServer("fixture", "1", srv.WithToolCapabilities(true), srv.WithHooks(newMCPHooks(logger)))
	response := server.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"__orank_nonexistent_tool_probe__","arguments":{"content":"private-payload-canary"}}}`))
	protocolError, ok := response.(mcpgo.JSONRPCError)
	require.True(t, ok)
	require.Equal(t, mcpgo.INVALID_PARAMS, protocolError.Error.Code)
	message := nextMCPAlert(t, messages)
	diagnostic := mcpAlertEnvelope(t, message)
	require.NotNil(t, diagnostic, "MCP failure must carry useful diagnostics instead of {}")
	require.Equal(t, "tools/call", diagnostic["method"])
	require.Equal(t, float64(3), diagnostic["request_id"])
	require.Equal(t, "tool_not_found", diagnostic["error_kind"])
	require.Equal(t, float64(mcpgo.INVALID_PARAMS), diagnostic["error_code"])
	require.NotEmpty(t, diagnostic["failure_id"])
	require.NotContains(t, message, "canary")
	require.NotContains(t, message, "__orank_nonexistent_tool_probe__")
	require.Contains(t, local.String(), "private-bound-canary")
	require.Contains(t, local.String(), "__orank_nonexistent_tool_probe__")
	require.Contains(t, local.String(), diagnostic["failure_id"])

	for _, method := range []string{"resources/list", "resources/templates/list", "prompts/list"} {
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 4, "method": method})
		require.NoError(t, err)
		result, ok := server.HandleMessage(ctx, request).(mcpgo.JSONRPCError)
		require.True(t, ok)
		require.Equal(t, mcpgo.METHOD_NOT_FOUND, result.Error.Code)
	}
	require.Contains(t, local.String(), "mcp request failed (non-critical)")
	require.Empty(t, mcpAlertsBefore(t, alert, messages), "one alert per source failure; expected probes stay quiet")

	server.AddTool(mcpgo.NewTool("fixture_failure"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return nil, errors.New("private-provider-error-canary")
	})
	result, ok := server.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"fixture_failure","arguments":{}}}`)).(mcpgo.JSONRPCError)
	require.True(t, ok)
	require.Equal(t, mcpgo.INTERNAL_ERROR, result.Error.Code)
	message = nextMCPAlert(t, messages)
	diagnostic = mcpAlertEnvelope(t, message)
	require.NotNil(t, diagnostic)
	require.Equal(t, float64(6), diagnostic["request_id"])
	require.Equal(t, "protocol_error", diagnostic["error_kind"])
	require.Equal(t, float64(mcpgo.INTERNAL_ERROR), diagnostic["error_code"])
	require.NotContains(t, message, "canary")
	require.Contains(t, local.String(), "private-provider-error-canary")
	require.Empty(t, mcpAlertsBefore(t, alert, messages), "a genuine handler failure sends exactly one alert")
}

// TestMCPFailureAlertReportsSDKMethods keeps real SDK method names that a fixed allowlist used to turn into "unknown".
func TestMCPFailureAlertReportsSDKMethods(t *testing.T) {
	ctx := context.Background()
	alert, messages := newMCPAlertFixture(t)
	var local bytes.Buffer
	// Production advertises tool capabilities only, so these methods fail through the SDK OnError hook.
	server := srv.NewMCPServer("fixture", "1", srv.WithToolCapabilities(true),
		srv.WithHooks(newMCPHooks(newMCPAlertLogger(t, productionMCPHooksLoggerName, alert, &local))))
	for _, tc := range []struct {
		method mcpgo.MCPMethod
		params any
		kind   string
		code   int
	}{
		{mcpgo.MethodTasksGet, map[string]any{"taskId": "private-task-canary"}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodTasksList, map[string]any{}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodTasksResult, map[string]any{"taskId": "private-task-canary"}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodTasksCancel, map[string]any{"taskId": "private-task-canary"}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodCompletionComplete, map[string]any{}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodResourcesRead, map[string]any{"uri": "private-uri-canary"}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodSetLogLevel, map[string]any{"level": "debug"}, "unsupported", mcpgo.METHOD_NOT_FOUND},
		{mcpgo.MethodToolsCall, "private-params-canary", "unparsable_request", mcpgo.INVALID_REQUEST},
	} {
		t.Run(string(tc.method)+"/"+tc.kind, func(t *testing.T) {
			request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": tc.method, "params": tc.params})
			require.NoError(t, err)
			response, ok := server.HandleMessage(ctx, request).(mcpgo.JSONRPCError)
			require.True(t, ok)
			require.Equal(t, tc.code, response.Error.Code)
			message := nextMCPAlert(t, messages)
			diagnostic := mcpAlertEnvelope(t, message)
			require.NotNil(t, diagnostic)
			require.Equal(t, string(tc.method), diagnostic["method"])
			require.Equal(t, tc.kind, diagnostic["error_kind"])
			require.Equal(t, float64(tc.code), diagnostic["error_code"])
			require.Equal(t, float64(9), diagnostic["request_id"])
			require.NotContains(t, message, "canary")
			require.Empty(t, mcpAlertsBefore(t, alert, messages))
		})
	}
}

// blockedMCPSession is a synthetic client session whose notification queue never drains.
type blockedMCPSession struct {
	notifications chan mcpgo.JSONRPCNotification
}

// Initialize marks the synthetic session ready.
func (blockedMCPSession) Initialize() {}

// Initialized reports the synthetic session ready for notifications.
func (blockedMCPSession) Initialized() bool { return true }

// NotificationChannel returns an unbuffered channel nobody reads.
func (s blockedMCPSession) NotificationChannel() chan<- mcpgo.JSONRPCNotification {
	return s.notifications
}

// SessionID returns a fixed synthetic session ID.
func (blockedMCPSession) SessionID() string { return "private-session-canary" }

// TestMCPFailureAlertReportsNotificationDelivery covers the SDK's notification pseudo-method hook failures.
func TestMCPFailureAlertReportsNotificationDelivery(t *testing.T) {
	alert, messages := newMCPAlertFixture(t)
	var local bytes.Buffer
	server := srv.NewMCPServer("fixture", "1", srv.WithToolCapabilities(true),
		srv.WithHooks(newMCPHooks(newMCPAlertLogger(t, productionMCPHooksLoggerName, alert, &local))))
	session := blockedMCPSession{notifications: make(chan mcpgo.JSONRPCNotification)}
	require.NoError(t, server.RegisterSession(context.Background(), session))
	err := server.SendNotificationToSpecificClient(session.SessionID(), "notifications/message", map[string]any{"data": "private-data-canary"})
	require.ErrorIs(t, err, srv.ErrNotificationChannelBlocked)
	message := nextMCPAlert(t, messages)
	diagnostic := mcpAlertEnvelope(t, message)
	require.NotNil(t, diagnostic)
	require.Equal(t, "notification", diagnostic["method"])
	require.Equal(t, "notification_channel_blocked", diagnostic["error_kind"])
	require.NotContains(t, diagnostic, "request_id")
	require.NotContains(t, diagnostic, "error_code", "a server-side delivery failure has no JSON-RPC wire code")
	require.NotContains(t, message, "canary")
}

// TestMCPFailureAlertProductionWiring sends a failing request through NewServer's HTTP handler and logger names.
func TestMCPFailureAlertProductionWiring(t *testing.T) {
	alert, messages := newMCPAlertFixture(t)
	var local bytes.Buffer
	root := newMCPAlertLogger(t, appLog.RootLoggerName, alert, &local).Named(appLog.ServiceLoggerScope)
	server, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, root)
	require.NoError(t, err)
	// tools/call needs a verified caller to reach the SDK (anonymous calls are
	// refused with 401 before it); TestMain installs an accept-all authority.
	response := performModernMCPRequest(t, server.Handler(), mcpgo.MethodToolsCall,
		map[string]any{"name": "__orank_nonexistent_tool_probe__", "arguments": map[string]any{"content": "private-payload-canary"}},
		http.Header{"Authorization": []string{"Bearer sk-alert-wiring-probe-1234567890"}})
	protocolErr, ok := decodeMCPResponse(t, response.Body.Bytes())["error"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, mcpgo.INVALID_PARAMS, protocolErr["code"])
	message := nextMCPAlert(t, messages)
	require.Contains(t, message, productionMCPHooksLoggerName)
	diagnostic := mcpAlertEnvelope(t, message)
	require.NotNil(t, diagnostic, "a renamed production scope or message must not silently revert the alert to {}")
	require.Equal(t, "tools/call", diagnostic["method"])
	require.Equal(t, "tool_not_found", diagnostic["error_kind"])
	require.Equal(t, float64(1), diagnostic["request_id"])
	require.NotContains(t, message, "canary")
	require.NotContains(t, message, "sk-alert-wiring-probe", "the alert never carries the caller's API key")
	require.Contains(t, local.String(), diagnostic["failure_id"])
	require.Empty(t, mcpAlertsBefore(t, alert, messages))
}

// TestMCPFailureTypedCauses never classifies an arbitrary error by its private message.
func TestMCPFailureTypedCauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"tool", errors.Wrap(srv.ErrToolNotFound, "private-canary"), "tool_not_found"},
		{"resource", errors.Wrap(srv.ErrResourceNotFound, "private-canary"), "resource_not_found"},
		{"prompt", errors.Wrap(srv.ErrPromptNotFound, "private-canary"), "prompt_not_found"},
		{"unsupported", errors.Wrap(srv.ErrUnsupported, "private-canary"), "unsupported"},
		{"unparsable", errors.WithStack(&srv.UnparsableMessageError{}), "unparsable_request"},
		{"notification", errors.Wrap(srv.ErrNotificationChannelBlocked, "private-canary"), "notification_channel_blocked"},
		{"canceled", errors.Wrap(context.Canceled, "private-canary"), "canceled"},
		{"deadline", errors.Wrap(context.DeadlineExceeded, "private-canary"), "deadline_exceeded"},
		{"unknown", errors.New("private-canary: tool not found"), "request_failed"},
		{"nil", nil, "request_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{})
			buffer, err := encoder.EncodeEntry(zapcore.Entry{}, []zapcore.Field{mcpRequestFailureDiagnostic(3, mcpgo.MethodToolsCall, tc.err)})
			require.NoError(t, err)
			defer buffer.Free()
			var value map[string]map[string]any
			require.NoError(t, json.Unmarshal(buffer.Bytes(), &value))
			require.Equal(t, tc.kind, value["mcp_failure"]["error_kind"])
			require.NotContains(t, value["mcp_failure"], "error_code", "no protocol code should be guessed")
			require.NotContains(t, buffer.String(), "canary")
		})
	}
}
