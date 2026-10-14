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
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// TestWebFetchWorkerDiagnosticsActualLogsAndAlert includes inherited logger and raw cause canaries.
func TestWebFetchWorkerDiagnosticsActualLogsAndAlert(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured bool
	}{{"worker", true}, {"unknown cause", false}} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan string, 1)
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
				_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
			}))
			t.Cleanup(endpoint.Close)
			alert, err := logSDK.NewAlert(context.Background(), endpoint.URL, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), appLog.WebFetchAlertOption())
			require.NoError(t, err)
			t.Cleanup(alert.Close)
			var local bytes.Buffer
			logger, err := logSDK.NewWithName("graphql.mcp.web_fetch", logSDK.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core {
					return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
				}), zap.HooksWithFields(appLog.WebFetchAlertHook(alert)))
			require.NoError(t, err)
			var billingCalls, fetchCalls int
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "private-token-canary" },
				func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil },
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					fetchCalls++
					cause := errors.WithStack(errors.New("private-worker-body-url-token-canary"))
					if !tc.structured {
						return nil, cause
					}
					var diag search.FetchFailureDiagnostic
					require.NoError(t, json.Unmarshal([]byte(`{"ErrorCode":"crawler_failed","Stage":"render","TaskID":"0199c118-4229-7000-8000-000000000002","WorkerStage":"conversion","WorkerCategory":"empty_markdown"}`), &diag))
					return nil, errors.WithStack(&search.FetchFailureError{Diagnostic: diag, Cause: cause})
				})
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("authorization", "private-auth-canary"),
				zap.String("url", "private-url-canary"), zap.String("remote_addr", "private-address-canary")))
			ctx = context.WithValue(ctx, ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
			result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/private-target-canary?token=private-query-canary"}}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, 1, billingCalls)
			require.Equal(t, 1, fetchCalls)
			require.NotContains(t, local.String(), "canary")
			require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "canary")
			decoder := json.NewDecoder(&local)
			var failures []map[string]any
			for decoder.More() {
				var entry map[string]any
				require.NoError(t, decoder.Decode(&entry))
				if entry["msg"] == "web_fetch failed" {
					failures = append(failures, entry)
				}
			}
			require.Len(t, failures, 1)
			require.Equal(t, "graphql.mcp.web_fetch", failures[0]["logger"])
			require.Equal(t, "0199c118-4229-7000-8000-000000000001", failures[0]["web_fetch_request_id"])
			if tc.structured {
				require.Equal(t, "conversion", failures[0]["web_fetch_worker_stage"])
				require.Equal(t, "empty_markdown", failures[0]["web_fetch_worker_category"])
			}
			select {
			case message := <-messages:
				require.NotContains(t, message, "canary")
				require.Contains(t, message, `"request_id":"0199c118-4229-7000-8000-000000000001"`)
				if tc.structured {
					require.Contains(t, message, `"error_code":"crawler_failed"`)
					require.Contains(t, message, `"worker_stage":"conversion"`)
					require.Contains(t, message, `"worker_category":"empty_markdown"`)
				} else {
					require.Contains(t, message, `"error_code":"fetch_failed"`)
					require.NotContains(t, message, "worker_")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("synthetic alert not delivered")
			}
		})
	}
}

func TestWebFetchBillingDiagnosticPrivacy(t *testing.T) {
	var local bytes.Buffer
	logger, err := logSDK.NewWithName("reviewed", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
	}))
	require.NoError(t, err)
	billingCalls, fetchCalls := 0, 0
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "private-token-canary" },
		func(context.Context, string, oneapi.Price, string) error {
			billingCalls++
			return errors.WithStack(errors.New("private-billing-body-token-canary"))
		}, func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) { fetchCalls++; return nil, nil })
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("authorization", "private-auth-canary")))
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/private-target-canary?token=private-query-canary"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, billingCalls)
	require.Zero(t, fetchCalls, "denied billing never invokes the worker")
	require.Equal(t, "billing check failed", result.Content[0].(mcp.TextContent).Text)
	require.NotContains(t, local.String(), "canary")
	require.Contains(t, local.String(), "web_fetch billing denied")
}
