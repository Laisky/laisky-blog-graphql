package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

type captureTestOutcome struct {
	result   *mcp.CallToolResult
	events   []map[string]any
	raw      []byte
	billings int
	fetches  int
}

// runCaptureTestHandler uses the actual handler and an ordinary JSON logger.
// It does not substitute a field observer for alert/collector acceptance.
func runCaptureTestHandler(t *testing.T, args map[string]any, authorized, success bool,
	hooks ...func(zapcore.Entry, []zapcore.Field) error,
) captureTestOutcome {
	t.Helper()
	var buffer bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buffer), zapcore.DebugLevel)
	options := []zap.Option{zap.WrapCore(func(zapcore.Core) zapcore.Core { return core })}
	for _, hook := range hooks {
		options = append(options, zap.HooksWithFields(hook))
	}
	logger, err := logSDK.NewWithName("graphql.laisky-graphql.mcp.web_fetch", logSDK.LevelDebug, options...)
	require.NoError(t, err)
	outcome := captureTestOutcome{}
	tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string {
		if authorized {
			return "synthetic-provider-auth-canary"
		}
		return ""
	}, func(context.Context, string, oneapi.Price, string) error {
		outcome.billings++
		return nil
	}, func(_ context.Context, _ *rlibs.DB, target, key string, markdown bool) ([]byte, error) {
		outcome.fetches++
		require.Equal(t, strings.TrimSpace(args["url"].(string)), target)
		require.Equal(t, "synthetic-provider-auth-canary", key)
		expectedMarkdown := true
		if declared, ok := args["output_markdown"].(bool); ok {
			expectedMarkdown = declared
		}
		require.Equal(t, expectedMarkdown, markdown)
		if success {
			return nil, nil // Preserve the established empty successful body.
		}
		return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{
			ErrorCode: "crawler_failed", Stage: "render", TaskID: "0199c118-4229-7000-8000-000000000002",
			WorkerStage: "conversion", WorkerCategory: "empty_markdown",
		}, Cause: errors.New("synthetic conversion failure")}
	})
	require.NoError(t, err)
	outcome.result, err = tool.Handle(context.Background(), mcp.CallToolRequest{
		Header: http.Header{"Authorization": {"Bearer synthetic-header-auth-canary"}},
		Params: mcp.CallToolParams{Arguments: args},
	})
	require.NoError(t, err)
	outcome.raw = bytes.Clone(buffer.Bytes())
	decoder := json.NewDecoder(bytes.NewReader(outcome.raw))
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else {
			require.NoError(t, err)
		}
		outcome.events = append(outcome.events, event)
	}
	return outcome
}

// captureFailureEvent requires exactly one ordinary failure event, with no archive references.
func captureFailureEvent(t *testing.T, outcome captureTestOutcome) map[string]any {
	t.Helper()
	var events []map[string]any
	for _, event := range outcome.events {
		if event["msg"] == "web_fetch failed" {
			events = append(events, event)
		}
		for key := range event {
			require.False(t, strings.HasPrefix(key, "failure_capture_"), key)
		}
	}
	require.Len(t, events, 1)
	return events[0]
}

// TestCrawlerFailureInputsDeclaredAndCorrelation preserves declared input and
// existing public error, stage, correlation and billing semantics.
func TestCrawlerFailureInputsDeclaredAndCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		present    bool
		value      any
		effective  bool
	}{
		{"absent", "absent", false, nil, true}, {"null", "null", true, nil, true},
		{"false", "bool", true, false, false}, {"true", "bool", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := " \thttps://1.1.1.1/Unicode-\u4f60\u597d?literal=synthetic-input-canary#fragment \n"
			args := map[string]any{"url": original, "unknown_api_key": "synthetic-unknown-auth-canary"}
			if tc.present {
				args["output_markdown"] = tc.value
			}
			outcome := runCaptureTestHandler(t, args, true, false)
			require.True(t, outcome.result.IsError)
			require.Equal(t, "fetch failed: crawler_failed", outcome.result.Content[0].(mcp.TextContent).Text)
			require.Equal(t, 1, outcome.billings)
			require.Equal(t, 1, outcome.fetches)
			event := captureFailureEvent(t, outcome)
			require.Equal(t, original, event["original_url"])
			require.Equal(t, strings.TrimSpace(original), event["submitted_url"])
			require.Equal(t, "utf8", event["original_url_encoding"])
			require.Equal(t, float64(len(original)), event["original_url_byte_length"])
			require.Equal(t, tc.present, event["output_markdown_declared"])
			require.Equal(t, tc.kind, event["declared_output_type"])
			require.Contains(t, event, "declared_output_markdown")
			require.Equal(t, tc.value, event["declared_output_markdown"])
			require.Equal(t, tc.effective, event["output_markdown"])
			require.NotEmpty(t, event["request_id"])
			require.Equal(t, "0199c118-4229-7000-8000-000000000002", event["task_id"])
			require.Equal(t, "conversion", event["stage"])
			require.Equal(t, "render", event["caller_stage"])
			require.Equal(t, "worker_result", event["caller_phase"])
			require.Equal(t, "conversion", event["effective_worker_stage"])
			require.Equal(t, "laisky-blog-graphql", event["origin_service"])
			require.NotEmpty(t, event["origin_hostname"])
			require.NotEmpty(t, event["origin_process_run_id"])
			require.NotEmpty(t, event["origin_go_version"])
			for _, canary := range []string{"synthetic-provider-auth-canary", "synthetic-header-auth-canary", "synthetic-unknown-auth-canary"} {
				require.NotContains(t, string(outcome.raw), canary)
			}
		})
	}
}

// TestCrawlerFailureInputsLargeAndBinary exercises actual ordinary JSON output,
// including admitted large whitespace, rejected large input and invalid UTF-8.
func TestCrawlerFailureInputsLargeAndBinary(t *testing.T) {
	for _, tc := range []struct {
		name, original string
		fetches        int
	}{
		{"large original whitespace", strings.Repeat(" \t", 32*1024) + "https://1.1.1.1/doc\n", 1},
		{"large rejected path", "https://1.1.1.1/" + strings.Repeat("synthetic-large-", 1000), 0},
		{"binary rejected path", "https://1.1.1.1/" + string([]byte{0xff, 0xfe, 0, '\n'}), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := runCaptureTestHandler(t, map[string]any{"url": tc.original}, true, false)
			require.True(t, outcome.result.IsError)
			require.Equal(t, tc.fetches, outcome.fetches)
			require.Equal(t, tc.fetches, outcome.billings)
			event := captureFailureEvent(t, outcome)
			require.Equal(t, float64(len(tc.original)), event["original_url_byte_length"])
			if utf8.ValidString(tc.original) {
				require.Equal(t, "utf8", event["original_url_encoding"])
				require.Equal(t, tc.original, event["original_url"])
			} else {
				require.Equal(t, "binary", event["original_url_encoding"])
				decoded, err := base64.StdEncoding.DecodeString(event["original_url"].(string))
				require.NoError(t, err)
				require.Equal(t, []byte(tc.original), decoded)
			}
		})
	}
}

// TestCrawlerFailureInputsSuccessAndAuthorization preserves no-input logging on
// successful and unauthenticated calls, with no extra failure events.
func TestCrawlerFailureInputsSuccessAndAuthorization(t *testing.T) {
	for _, authorized := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "missing authorization"}[authorized], func(t *testing.T) {
			outcome := runCaptureTestHandler(t, map[string]any{"url": "https://1.1.1.1/doc"}, authorized, true)
			require.Equal(t, !authorized, outcome.result.IsError)
			for _, event := range outcome.events {
				require.NotContains(t, event, "original_url")
				require.NotEqual(t, "web_fetch failed", event["msg"])
			}
			if authorized {
				require.Equal(t, 1, outcome.billings)
				require.Equal(t, 1, outcome.fetches)
			} else {
				require.Zero(t, outcome.billings)
				require.Zero(t, outcome.fetches)
				require.Equal(t, "missing authorization bearer token", outcome.result.Content[0].(mcp.TextContent).Text)
			}
		})
	}
}

// captureDefaultSDKHook uses the normal production hook and actual SDK HTTP transport.
func captureDefaultSDKHook(t *testing.T) (func(zapcore.Entry, []zapcore.Field) error, <-chan string) {
	t.Helper()
	messages := make(chan string, 4)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var envelope struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Error(err)
			return
		}
		var message string
		if err := json.Unmarshal(envelope.Variables["msg"], &message); err != nil {
			t.Error(err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
	}))
	t.Cleanup(peer.Close)
	alert, err := logSDK.NewAlert(context.Background(), peer.URL, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-alert-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	return appLog.WebDiagnosticAlertHook(alert.GetZapHook()), messages
}

// TestCrawlerFailureInputsDefaultSDKMetadataDelivery checks actual metadata
// delivery only. It does not establish full-input fidelity through the SDK.
func TestCrawlerFailureInputsDefaultSDKMetadataDelivery(t *testing.T) {
	hook, messages := captureDefaultSDKHook(t)
	outcome := runCaptureTestHandler(t, map[string]any{"url": "https://1.1.1.1/doc"}, true, false, hook)
	require.True(t, outcome.result.IsError)
	select {
	case message := <-messages:
		require.Contains(t, message, `"origin_service":"laisky-blog-graphql"`)
		require.Contains(t, message, `"task_id":"0199c118-4229-7000-8000-000000000002"`)
		require.Contains(t, message, `"caller_phase":"worker_result"`)
		require.NotContains(t, message, "synthetic-provider-auth-canary")
	case <-time.After(3 * time.Second):
		t.Fatal("default SDK did not deliver the ordinary failure metadata")
	}
}

// TestCrawlerFailureInputsTaskIdentityProjection keeps diagnostic projection in
// charge of correlation instead of reintroducing an omitted helper input.
func TestCrawlerFailureInputsTaskIdentityProjection(t *testing.T) {
	const canonicalTaskID = "0199c118-4229-7000-8000-000000000002"
	for _, tc := range []struct {
		name, diagnosticTaskID, projectedTaskID string
	}{
		{"invalid omitted", "synthetic-unbound-task-canary", ""},
		{"canonical omitted", canonicalTaskID, ""},
		{"validated details retained", "synthetic-unbound-task-canary", canonicalTaskID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buffer), zapcore.DebugLevel)
			logger := zap.New(core)
			capture := newWebFetchCapture(mcp.CallToolRequest{Params: mcp.CallToolParams{
				Arguments: map[string]any{"url": "https://1.1.1.1/doc"},
			}}, "https://1.1.1.1/doc", "")
			details := []zapcore.Field{zap.String("error_code", "crawler_failed"), zap.String("stage", "conversion")}
			if tc.projectedTaskID != "" {
				details = append(details, zap.String("task_id", tc.projectedTaskID))
			}
			logger.Error("web_fetch failed", capture.writeFailure(search.FetchFailureDiagnostic{
				ErrorCode: "crawler_failed", Stage: "render", TaskID: tc.diagnosticTaskID,
			}, details...)...)
			event := ordinaryWebFetchFailureJSON(t, buffer.Bytes())
			require.True(t, capture.attempted)
			require.Equal(t, "https://1.1.1.1/doc", event["original_url"])
			require.Equal(t, "conversion", event["stage"])
			require.NotContains(t, buffer.String(), "synthetic-unbound-task-canary")
			if tc.projectedTaskID == "" {
				require.NotContains(t, event, "task_id", "an omitted projection must stay omitted")
				require.Zero(t, bytes.Count(buffer.Bytes(), []byte(`"task_id":`)))
			} else {
				require.Equal(t, tc.projectedTaskID, event["task_id"])
				require.Equal(t, 1, bytes.Count(buffer.Bytes(), []byte(`"task_id":`)), "validated details must be retained exactly once")
			}
		})
	}
}
