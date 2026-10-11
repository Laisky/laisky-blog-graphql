package log

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

func alertReceiver(t *testing.T) (string, <-chan string) {
	t.Helper()
	messages := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode synthetic alert: %v", err)
			return
		}
		var message string
		if err := json.Unmarshal(request.Variables["msg"], &message); err != nil {
			t.Errorf("decode synthetic message: %v", err)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
	}))
	t.Cleanup(server.Close)
	return server.URL, messages
}

func receivedAlert(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic alert was not delivered")
		return ""
	}
}

func TestWebFetchAlertDefaultSDKOmitsLocalStructuredFields(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	require.NoError(t, alert.GetZapHook()(entry, []zapcore.Field{zap.Error(errors.New("private-error-canary")), zap.String("url", "private-url-canary")}))
	message := receivedAlert(t, messages)
	require.True(t, strings.HasSuffix(message, "{}"))
	require.NotContains(t, message, "private-error-canary")
	require.NotContains(t, message, "private-url-canary")
}

func TestWebFetchAlertReviewedFieldsOnly(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"), WebFetchAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	hook := WebFetchAlertHook(alert)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	fields := WebFetchFailureFields(WebFetchFailureSummary{
		ErrorCode: "crawler_egress_unverified", Stage: "verification", RequestID: "0199c118-4229-7000-8000-000000000001",
		TaskID: "0199c118-4229-7000-8000-000000000002", DurationMS: 5000,
	})
	fields = append(fields, zap.Error(errors.New("private-error-canary")), zap.String("url", "private-url-canary"),
		zap.String("request_id", "private-id-canary"), zap.String("error_code", "private-code-canary"))
	require.NoError(t, hook(entry, fields))
	message := receivedAlert(t, messages)
	for _, expected := range []string{`"error_code":"crawler_egress_unverified"`, `"stage":"verification"`, `"duration_ms":5000`, `"request_id":"0199c118-4229-7000-8000-000000000001"`, `"task_id":"0199c118-4229-7000-8000-000000000002"`} {
		require.Contains(t, message, expected)
	}
	require.NotContains(t, message, "canary")

	entry.LoggerName = "graphql.other"
	require.NoError(t, hook(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"), "unrelated callsites retain metadata-only alerts")
	entry.LoggerName = "graphql.mcp.web_fetch"
	entry.Message = "other static failure"
	require.NoError(t, hook(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
}

func TestWebFetchAlertRejectsUnreviewedSummary(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"), WebFetchAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	fields := []zapcore.Field{zap.String("web_fetch_error_code", "private-code-canary"), zap.String("web_fetch_stage", "private-stage-canary"),
		zap.String("web_fetch_request_id", "private-id-canary"), zap.String("web_fetch_task_id", strings.Repeat("a", 4096)), zap.Int64("web_fetch_duration_ms", -1)}
	require.NoError(t, WebFetchAlertHook(alert)(entry, fields))
	message := receivedAlert(t, messages)
	require.Contains(t, message, `"error_code":"fetch_failed"`)
	require.Contains(t, message, `"stage":"fetch"`)
	require.NotContains(t, message, "canary")
	require.NotContains(t, message, "request_id")
	require.NotContains(t, message, "task_id")
	require.NotContains(t, message, "duration_ms")
	fields = append(fields, zap.String("web_fetch_stage", "verification"))
	require.NoError(t, WebFetchAlertHook(alert)(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"), "duplicate dedicated fields fail closed")
}
