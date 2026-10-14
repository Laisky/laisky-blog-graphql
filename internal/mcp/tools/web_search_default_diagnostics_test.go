package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

func searchDefaultAlertReceiver(t *testing.T) (string, <-chan string) {
	t.Helper()
	messages := make(chan string, 2)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var message string
		if err := json.Unmarshal(body.Variables["msg"], &message); err != nil {
			t.Error(err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"fixture"}}}`))
	}))
	t.Cleanup(endpoint.Close)
	return endpoint.URL, messages
}

func receiveSearchDefaultAlert(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("actual SDK HTTP alert not delivered")
		return ""
	}
}

// TestWebSearchDefaultDiagnosticsMeaningfulCauseAndAliases exercises the application scope with the SDK default.
func TestWebSearchDefaultDiagnosticsMeaningfulCauseAndAliases(t *testing.T) {
	endpoint, messages := searchDefaultAlertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("fixture"), logSDK.WithAlertToken("fixture-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	var local bytes.Buffer
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
	}), zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
	require.NoError(t, err)
	cause := errors.WithStack(errors.New("navigation failed: unexpected EOF; api_key=private-key-canary; response body: private-page-canary"))
	provider := &diagnosticSearchProvider{err: cause}
	billingCalls := 0
	tool, err := NewWebSearchTool(provider, logger, func(context.Context) string { return "private-token-canary" },
		func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil })
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("query", "private-query-canary"), zap.String("authorization", "private-auth-canary")))
	ctx = context.WithValue(ctx, ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "private-user-query-canary"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, billingCalls)
	message := receiveSearchDefaultAlert(t, messages)
	require.Contains(t, message, "unexpected EOF", "actual serialized alert must retain the useful cause")
	require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "canary")
	decoder := json.NewDecoder(bytes.NewReader(local.Bytes()))
	var failure map[string]any
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if entry["msg"] == "web_search failed" {
			require.Nil(t, failure)
			failure = entry
		}
	}
	require.NotNil(t, failure)
	require.Contains(t, failure["error"], "unexpected EOF", "ordinary internal cause must survive source logging")
	require.Equal(t, "provider", failure["stage"])
	require.Equal(t, "unknown", failure["category"], "cause text is not classification evidence")
	require.Equal(t, "0199c118-4229-7000-8000-000000000001", failure["request_id"])
	require.Contains(t, failure, "duration_ms")
	require.NotContains(t, failure, "web_search_stage", "authored aliases are emitted once")
	require.NotContains(t, local.String(), "canary")

	for _, fragment := range []string{`"stage":"provider"`, `"category":"unknown"`, `"request_id":"0199c118-4229-7000-8000-000000000001"`, `"duration_ms":`} {
		require.Contains(t, message, fragment)
	}
	require.NotContains(t, message, "web_search_")
	require.NotContains(t, message, "canary")
	// An ordinary future field stays useful within the reviewed scope without a field-name list.
	logger.Named("web_search").Error("other diagnostic failed", zap.String("future_diagnostic_revision", "parser-v2"), zap.Int("failure_count", 3))
	message = receiveSearchDefaultAlert(t, messages)
	require.Contains(t, message, `"future_diagnostic_revision":"parser-v2"`)
	require.Contains(t, message, `"failure_count":3`)
	require.Contains(t, local.String(), `"future_diagnostic_revision":"parser-v2"`)
}

// TestWebSearchDefaultDiagnosticsTypedBillingBody preserves status without the remote body.
func TestWebSearchDefaultDiagnosticsTypedBillingBody(t *testing.T) {
	var local bytes.Buffer
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
	}))
	require.NoError(t, err)
	provider := &diagnosticSearchProvider{}
	billing := &oneapi.BillingError{Outcome: oneapi.BillingDenied, Status: 429, Reason: "private-unlabeled-user-response-canary"}
	tool, err := NewWebSearchTool(provider, logger, func(context.Context) string { return "fixture-token" },
		func(context.Context, string, oneapi.Price, string) error { return errors.WithStack(billing) })
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "private-query-canary"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Empty(t, provider.requestID, "denied billing never invokes the provider")
	require.Contains(t, local.String(), "HTTP 429", "typed status remains useful without raw Reason")
	require.Contains(t, local.String(), string(oneapi.BillingDenied))
	require.NotContains(t, local.String(), "canary")
	require.Equal(t, "private-unlabeled-user-response-canary", billing.Reason)
}
