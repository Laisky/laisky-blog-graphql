package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// TestCrawlerFailureContextActualDefaultAlert exercises the real tool, production
// hook and default SDK HTTP serializer, not the legacy allowlist adapter.
func TestCrawlerFailureContextActualDefaultAlert(t *testing.T) {
	messages := make(chan string, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var variables map[string]json.RawMessage
		if err := json.Unmarshal(body["variables"], &variables); err != nil {
			t.Error(err)
			return
		}
		var message string
		if err := json.Unmarshal(variables["msg"], &message); err != nil {
			t.Error(err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"TelegramMonitorAlert": map[string]any{"name": "synthetic"}}})
	}))
	t.Cleanup(peer.Close)
	alert, err := logSDK.NewAlert(context.Background(), peer.URL, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-alert-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	var local bytes.Buffer
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core {
			return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
		}),
		zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
	require.NoError(t, err)
	billingCalls, fetchCalls := 0, 0
	workerContext := map[string]any{"final_scheme": "https",
		"final_hostname":                  "example.net",
		"upstream_status":                 200,
		"content_type":                    "text/html",
		"redirect_count":                  1,
		"rendered_html_bytes":             1234,
		"conversion_input_bytes":          456,
		"conversion_output_bytes":         0,
		"conversion_output_trimmed_bytes": 0,
		"body_text_runes":                 0,
		"body_text_nodes":                 0,
		"converter":                       "html-to-markdown/v2"}
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-local-billing-key" },
		func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			fetchCalls++
			raw, err := json.Marshal(map[string]any{"ErrorCode": "crawler_failed", "Stage": "render", "TaskID": "0199c118-4229-7000-8000-000000000002", "WorkerStage": "conversion", "WorkerCategory": "empty_markdown", "WorkerContext": workerContext})
			require.NoError(t, err)
			var diagnostic search.FetchFailureDiagnostic
			require.NoError(t, json.Unmarshal(raw, &diagnostic))
			return nil, errors.WithStack(&search.FetchFailureError{Diagnostic: diagnostic, Cause: errors.New("crawler task failed")})
		})
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/private-path-canary?token=private-query-canary"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, "fetch failed: crawler_failed", result.Content[0].(mcp.TextContent).Text)
	require.Equal(t, 1, billingCalls)
	require.Equal(t, 1, fetchCalls)
	var message string
	select {
	case message = <-messages:
	case <-time.After(3 * time.Second):
		t.Fatal("actual default SDK alert missing")
	}
	expected := map[string]any{"stage": "conversion", "caller_stage": "render", "caller_phase": "worker_result", "effective_worker_stage": "conversion",
		"error_code": "crawler_failed", "worker_category": "empty_markdown", "request_id": "0199c118-4229-7000-8000-000000000001", "task_id": "0199c118-4229-7000-8000-000000000002",
		"target_scheme": "https", "target_hostname": "1.1.1.1"}
	for key, value := range workerContext {
		expected[key] = value
	}
	for key, value := range expected {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.Contains(t, message, fmt.Sprintf("%q:%s", key, encoded))
	}
	require.NotContains(t, message, "canary")
	require.NotContains(t, local.String(), "canary")
}
