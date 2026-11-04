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

// TestWebFetchChallengeSignalActualLogsAndAlert observes the actual tools
// callsite's JSON and SDK HTTP serialization, including private negative controls.
// Billing and fetch are local injected fixtures, and no provider is contacted.
func TestWebFetchChallengeSignalActualLogsAndAlert(t *testing.T) {
	for _, tc := range []struct {
		name, workerStage, category, signal, task string
		local, remote, legacy                     bool
	}{
		{"widget", "render", "challenge", "widget_element", "0199c118-4229-7000-8000-000000000002", true, true, false},
		{"error", "render", "challenge", "error_element", "0199c118-4229-7000-8000-000000000002", true, true, false},
		{"platform", "render", "challenge", "challenge_platform_script", "0199c118-4229-7000-8000-000000000002", true, true, false},
		{"options", "render", "challenge", "challenge_options_assignment", "0199c118-4229-7000-8000-000000000002", true, true, false},
		{"unknown", "render", "challenge", "private-signal-canary https://private.invalid/?key=secret", "0199c118-4229-7000-8000-000000000002", false, false, false},
		{"nonchallenge", "render", "render_failed", "widget_element", "0199c118-4229-7000-8000-000000000002", false, false, false},
		{"wrong stage", "conversion", "empty_body", "widget_element", "0199c118-4229-7000-8000-000000000002", false, false, false},
		{"invalid correlation", "render", "challenge", "widget_element", "private-task-canary", false, false, false},
		{"legacy payload", "render", "challenge", "", "0199c118-4229-7000-8000-000000000002", false, false, false},
		{"legacy explicit filter", "render", "challenge", "widget_element", "0199c118-4229-7000-8000-000000000002", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan string, 1)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var payload struct {
					Variables struct {
						Message string `json:"msg"`
					} `json:"variables"`
				}
				if decodeErr := json.NewDecoder(request.Body).Decode(&payload); decodeErr != nil {
					t.Errorf("decode synthetic alert: %v", decodeErr)
					http.Error(w, "invalid synthetic alert", http.StatusBadRequest)
					return
				}
				messages <- payload.Variables.Message
				w.Header().Set("Content-Type", "application/json")
				_, writeErr := w.Write([]byte("{\"data\":{\"TelegramMonitorAlert\":{\"name\":\"synthetic\"}}}"))
				if writeErr != nil {
					t.Errorf("write synthetic alert response: %v", writeErr)
				}
			}))
			t.Cleanup(endpoint.Close)
			options := []logSDK.AlertOption{logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic")}
			if tc.legacy {
				options = append(options, appLog.WebFetchAlertOption())
			}
			alert, err := logSDK.NewAlert(context.Background(), endpoint.URL, options...)
			require.NoError(t, err)
			t.Cleanup(func() {
				alert.Close()
				select {
				case <-alert.Done():
				case <-time.After(3 * time.Second):
					t.Error("synthetic alert sender failed to stop")
				}
			})
			var local bytes.Buffer
			logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core {
					return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
				}), zap.HooksWithFields(appLog.WebDiagnosticAlertHook(alert.GetZapHook())))
			require.NoError(t, err)
			billingCalls, fetchCalls := 0, 0
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "private-token-canary" },
				func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil },
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					fetchCalls++
					wire, marshalErr := json.Marshal(map[string]string{
						"ErrorCode": "crawler_failed", "Stage": "render", "TaskID": tc.task,
						"WorkerStage": tc.workerStage, "WorkerCategory": tc.category, "WorkerSignal": tc.signal,
					})
					require.NoError(t, marshalErr)
					var diagnostic search.FetchFailureDiagnostic
					require.NoError(t, json.Unmarshal(wire, &diagnostic))
					cause := errors.New("navigation failed; Authorization: Bearer private-auth-canary; response body: private-body-canary")
					return nil, errors.WithStack(&search.FetchFailureError{Diagnostic: diagnostic, Cause: cause})
				})
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
			ctx = context.WithValue(ctx, ctxkeys.Logger, logger.With(zap.String("authorization", "private-inherited-canary")))
			originalURL := "https://1.1.1.1/private-target-canary?key=private-query-canary"
			result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
				"url": originalURL, "unknown_config": "private-config-canary", "unknown_input": "private-unknown-input-canary",
			}}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, 1, billingCalls)
			require.Equal(t, 1, fetchCalls)
			require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "canary")
			localText := local.String()
			// Selected URL/query bytes remain; unrelated credentials and worker data do not.
			forbidden := []string{"private-token-canary", "private-auth-canary", "private-body-canary",
				"private-inherited-canary", "private-signal-canary", "private-task-canary", "private.invalid",
				"private-config-canary", "private-unknown-input-canary"}
			for _, value := range forbidden {
				require.NotContains(t, localText, value)
			}
			decoder := json.NewDecoder(&local)
			var failure map[string]any
			count := 0
			for decoder.More() {
				var entry map[string]any
				require.NoError(t, decoder.Decode(&entry))
				if entry["msg"] == "web_fetch failed" {
					failure = entry
					count++
				}
			}
			require.Equal(t, 1, count)
			require.Equal(t, "crawler_failed", failure["error_code"])
			requireWebFetchChosenInput(t, failure, originalURL)
			if tc.task == "0199c118-4229-7000-8000-000000000002" {
				require.Equal(t, tc.task, failure["task_id"])
			} else {
				require.NotContains(t, failure, "task_id", "invalid correlation must stay omitted by the diagnostic projection")
			}
			if tc.local {
				require.Equal(t, tc.signal, failure["worker_signal"])
			} else {
				require.NotContains(t, failure, "worker_signal")
			}
			select {
			case message := <-messages:
				for _, value := range forbidden {
					require.NotContains(t, message, value)
				}
				require.Contains(t, message, "\"error_code\":\"crawler_failed\"")
				if tc.remote {
					require.Contains(t, message, "\"worker_signal\":\""+tc.signal+"\"")
				} else {
					require.NotContains(t, message, "\"worker_signal\":")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("actual local SDK HTTP alert was not delivered")
			}
		})
	}
}
