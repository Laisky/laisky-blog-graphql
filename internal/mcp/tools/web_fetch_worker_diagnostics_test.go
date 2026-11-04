package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
			alert, err := logSDK.NewAlert(context.Background(), endpoint.URL, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"))
			require.NoError(t, err)
			t.Cleanup(alert.Close)
			var local bytes.Buffer
			logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core {
					return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
				}), zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
			require.NoError(t, err)
			var billingCalls, fetchCalls int
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "private-token-canary" },
				func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil },
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					fetchCalls++
					cause := errors.WithStack(errors.New("navigation failed: unexpected EOF; Authorization: Bearer private-auth-token-canary; response body: private-worker-body-url-token-canary"))
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
			originalURL := "https://1.1.1.1/private-target-canary?token=private-query-canary"
			result, err := tool.Handle(ctx, mcp.CallToolRequest{Header: http.Header{"Authorization": {"Bearer private-header-canary"}},
				Params: mcp.CallToolParams{Arguments: map[string]any{"url": originalURL,
					"unknown_config": "private-config-canary", "unknown_input": "private-unknown-input-canary"}}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, 1, billingCalls)
			require.Equal(t, 1, fetchCalls)
			forbidden := []string{"private-token-canary", "private-auth-canary", "private-auth-token-canary",
				"private-worker-body-url-token-canary", "private-url-canary", "private-address-canary",
				"private-header-canary", "private-config-canary", "private-unknown-input-canary"}
			for _, value := range forbidden {
				require.NotContains(t, local.String(), value)
			}
			require.Contains(t, local.String(), "unexpected EOF")
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
			require.Equal(t, "graphql.laisky-graphql.mcp.web_fetch", failures[0]["logger"])
			require.Equal(t, "0199c118-4229-7000-8000-000000000001", failures[0]["request_id"])
			requireWebFetchChosenInput(t, failures[0], originalURL)
			if tc.structured {
				require.Equal(t, "conversion", failures[0]["worker_stage"])
				require.Equal(t, "empty_markdown", failures[0]["worker_category"])
			}
			select {
			case message := <-messages:
				for _, value := range forbidden {
					require.NotContains(t, message, value)
				}
				require.Contains(t, message, "unexpected EOF")
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
			return errors.WithStack(errors.New("billing quota check failed; response body: private-billing-body-token-canary"))
		}, func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) { fetchCalls++; return nil, nil })
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("authorization", "private-auth-canary")))
	originalURL := "https://1.1.1.1/private-target-canary?token=private-query-canary"
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": originalURL}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, billingCalls)
	require.Zero(t, fetchCalls, "denied billing never invokes the worker")
	require.Equal(t, "billing check failed", result.Content[0].(mcp.TextContent).Text)
	requireWebFetchChosenInput(t, ordinaryWebFetchFailureJSON(t, local.Bytes()), originalURL)
	for _, value := range []string{"private-token-canary", "private-auth-canary", "private-billing-body-token-canary"} {
		require.NotContains(t, local.String(), value)
	}
	require.Contains(t, local.String(), "web_fetch billing denied")
}

func TestWebFetchTypedBillingBodyIsNotDiagnosticText(t *testing.T) {
	var local bytes.Buffer
	logger, err := logSDK.NewWithName("reviewed", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
	}))
	require.NoError(t, err)
	billing := &oneapi.BillingError{Outcome: oneapi.BillingDenied, Status: 429, Reason: "private-unlabeled-user-page-canary"}
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-token" },
		func(context.Context, string, oneapi.Price, string) error { return errors.WithStack(billing) },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			t.Fatal("denied billing must not fetch")
			return nil, nil
		})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/doc"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, local.String(), "HTTP 429")
	require.Contains(t, local.String(), string(oneapi.BillingDenied))
	requireWebFetchChosenInput(t, ordinaryWebFetchFailureJSON(t, local.Bytes()), "https://1.1.1.1/doc")
	require.NotContains(t, local.String(), "private-unlabeled-user-page-canary")
	require.Equal(t, "private-unlabeled-user-page-canary", billing.Reason, "original billing result is untouched")
}

// ordinaryWebFetchFailureJSON reads the actual ordinary JSON sink and requires one failure event.
func ordinaryWebFetchFailureJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var failures []map[string]any
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else {
			require.NoError(t, err)
		}
		if event["msg"] == "web_fetch failed" {
			failures = append(failures, event)
		}
	}
	require.Len(t, failures, 1)
	return failures[0]
}

// requireWebFetchChosenInput distinguishes selected URL/options from unrelated secret data.
func requireWebFetchChosenInput(t *testing.T, event map[string]any, originalURL string) {
	t.Helper()
	require.Equal(t, originalURL, event["original_url"])
	require.Equal(t, strings.TrimSpace(originalURL), event["submitted_url"])
	require.Equal(t, "utf8", event["original_url_encoding"])
	require.Equal(t, "utf8", event["submitted_url_encoding"])
	require.Equal(t, float64(len(originalURL)), event["original_url_byte_length"])
	require.Equal(t, float64(len(strings.TrimSpace(originalURL))), event["submitted_url_byte_length"])
	require.Equal(t, false, event["output_markdown_declared"])
	require.Equal(t, "absent", event["declared_output_type"])
	require.Contains(t, event, "declared_output_markdown")
	require.Nil(t, event["declared_output_markdown"])
	require.Equal(t, true, event["output_markdown"])
}
