package log

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

func TestWebSearchAlertBaselineAndReviewedSerialization(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), ToolFailureAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.laisky-graphql.mcp.web_search", Message: "web_search failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	fields := WebSearchFailureFields(WebSearchFailureSummary{Diagnostic: diagnostics.Summary(diagnostics.WithAttempts(diagnostics.WithProvider(diagnostics.HTTP(429), "google"), 2)), RequestID: "0199c118-4229-7000-8000-000000000001", DurationMS: 5000})
	fields = append(fields, zap.Error(errors.New("private-canary")), zap.String("category", "private-canary"), zap.String("url", "private-canary"))
	require.NoError(t, WebFetchAlertHook(alert)(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"), "the previous application hook drops search fields")
	require.NoError(t, ToolFailureAlertHook(alert)(entry, fields))
	message := receivedAlert(t, messages)
	for _, fragment := range []string{`"category":"rate_limit"`, `"stage":"response"`, `"status":429`, `"provider":"google"`, `"attempts":2`, `"duration_ms":5000`, `"request_id":"0199c118-4229-7000-8000-000000000001"`} {
		require.Contains(t, message, fragment)
	}
	require.NotContains(t, message, "canary")
	entry.LoggerName = "graphql.other"
	require.NoError(t, ToolFailureAlertHook(alert)(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
}

func TestWebSearchAlertRejectsUntrustedFields(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), ToolFailureAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_search", Message: "web_search failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	fields := []zapcore.Field{zap.String("web_search_category", strings.Repeat("private-canary", 4096)), zap.String("web_search_stage", "private-canary"), zap.String("web_search_provider", "private-canary"), zap.String("web_search_request_id", "private-canary"), zap.Int64("web_search_duration_ms", -1), zap.Int64("web_search_status", 999), zap.Int64("web_search_attempts", 999)}
	hook := ToolFailureAlertHook(alert)
	require.NoError(t, hook(entry, fields))
	message := receivedAlert(t, messages)
	require.Contains(t, message, `"category":"unknown"`)
	require.NotContains(t, message, "canary")
	require.NotContains(t, message, "request_id")
	require.NotContains(t, message, "status")
	require.NotContains(t, message, "duration_ms")
	fields = append(fields, zap.String("web_search_category", "auth"))
	require.NoError(t, hook(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
}

type searchAlertObject struct{ called *bool }

func (o searchAlertObject) MarshalLogObject(zapcore.ObjectEncoder) error {
	*o.called = true
	return nil
}

func TestWebSearchAlertNeverInvokesUntrustedMarshalers(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), ToolFailureAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_search", Message: "web_search failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	called := false
	fields := []zapcore.Field{zap.Object("web_search_category", searchAlertObject{called: &called}), zap.Namespace("private-canary"), zap.String("web_search_stage", "private-canary")}
	require.NoError(t, ToolFailureAlertHook(alert)(entry, fields))
	message := receivedAlert(t, messages)
	require.False(t, called)
	require.NotContains(t, message, "canary")
	require.Contains(t, message, `"category":"unknown"`)
}

func TestToolFailureAlertPreservesFetchEnvelope(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), ToolFailureAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	fields := WebFetchFailureFields(WebFetchFailureSummary{ErrorCode: "fetch_timeout", Stage: "wait", RequestID: "0199c118-4229-7000-8000-000000000001", TaskID: "0199c118-4229-7000-8000-000000000002", DurationMS: 5000})
	fields = append(fields, zap.String("category", "private-canary"), zap.String("web_search_category", "private-canary"), zap.Error(errors.New("private-canary")))
	require.NoError(t, ToolFailureAlertHook(alert)(entry, fields))
	message := receivedAlert(t, messages)
	for _, fragment := range []string{`"error_code":"fetch_timeout"`, `"stage":"wait"`, `"duration_ms":5000`, `"request_id":"0199c118-4229-7000-8000-000000000001"`, `"task_id":"0199c118-4229-7000-8000-000000000002"`} {
		require.Contains(t, message, fragment)
	}
	require.NotContains(t, message, "category")
	require.NotContains(t, message, "canary")
}
