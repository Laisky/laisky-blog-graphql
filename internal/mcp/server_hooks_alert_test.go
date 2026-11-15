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
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestMCPRequestFailureAlert exercises the real dependency error hook and alert HTTP serializer.
func TestMCPRequestFailureAlert(t *testing.T) {
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
	defer endpoint.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alert, err := logSDK.NewAlert(ctx, endpoint.URL, logSDK.WithAlertType("fixture"), logSDK.WithAlertToken("synthetic-token"))
	require.NoError(t, err)
	defer func() { alert.Close(); <-alert.Done() }()
	var local bytes.Buffer
	logger, err := logSDK.New(logSDK.WithName("graphql.laisky-graphql.mcp_hooks"), logSDK.WithLevel(logSDK.LevelDebug), logSDK.WithOutputPaths([]string{}),
		logSDK.WithZapOptions(zap.WrapCore(func(zapcore.Core) zapcore.Core {
			return zapcore.NewTee(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zap.DebugLevel), appLog.WebDiagnosticAlertCore(alert))
		})))
	require.NoError(t, err)
	logger = logger.With(zap.String("private_bound", "private-bound-canary"))
	server := srv.NewMCPServer("fixture", "1", srv.WithToolCapabilities(true), srv.WithHooks(newMCPHooks(logger)))
	response := server.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"__orank_nonexistent_tool_probe__","arguments":{"content":"private-payload-canary"}}}`))
	protocolError, ok := response.(mcpgo.JSONRPCError)
	require.True(t, ok)
	require.Equal(t, mcpgo.INVALID_PARAMS, protocolError.Error.Code)
	var message string
	select {
	case message = <-messages:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP error alert missing")
	}
	offset := strings.LastIndex(message, "\n{")
	require.GreaterOrEqual(t, offset, 0)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(message[offset+1:]), &fields))
	diagnostic, ok := fields["mcp_failure"].(map[string]any)
	require.True(t, ok, "MCP failure must carry useful diagnostics instead of {}")
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
	server.AddTool(mcpgo.NewTool("fixture_failure"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return nil, errors.New("private-provider-error-canary")
	})
	result, ok := server.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"fixture_failure","arguments":{}}}`)).(mcpgo.JSONRPCError)
	require.True(t, ok)
	require.Equal(t, mcpgo.INTERNAL_ERROR, result.Error.Code)
	select {
	case message = <-messages:
	case <-time.After(3 * time.Second):
		t.Fatal("genuine handler failure alert missing")
	}
	offset = strings.LastIndex(message, "\n{")
	require.GreaterOrEqual(t, offset, 0)
	require.NoError(t, json.Unmarshal([]byte(message[offset+1:]), &fields))
	diagnostic = fields["mcp_failure"].(map[string]any)
	require.Equal(t, float64(6), diagnostic["request_id"])
	require.Equal(t, "protocol_error", diagnostic["error_kind"])
	require.Equal(t, float64(mcpgo.INTERNAL_ERROR), diagnostic["error_code"])
	require.NotContains(t, message, "canary")
	require.Contains(t, local.String(), "private-provider-error-canary")
	alert.Close()
	<-alert.Done()
	require.Empty(t, messages, "noncritical probes stay quiet and each source failure sends one alert")

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
