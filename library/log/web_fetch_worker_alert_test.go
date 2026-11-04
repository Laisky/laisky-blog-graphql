package log

import (
	"context"
	"strings"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

type untrustedFetchObject struct{ called *bool }

func (o untrustedFetchObject) MarshalLogObject(zapcore.ObjectEncoder) error {
	*o.called = true
	return nil
}

// TestWebFetchWorkerAlertActualSerialization covers the SDK's emitted HTTP alert payload.
func TestWebFetchWorkerAlertActualSerialization(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), WebFetchAlertOption())
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	entry := zapcore.Entry{LoggerName: "graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	hook := WebFetchAlertHook(alert)
	for _, tc := range []struct {
		name, stage, category string
		approved              bool
	}{
		{"conversion", "conversion", "empty_markdown", true},
		{"challenge", "render", "challenge", true},
		{"legacy", "", "", false},
		{"freeform", "render", "private-canary", false},
		{"oversized", strings.Repeat("private-canary", 4096), "challenge", false},
		{"mismatched pair", "render", "conversion_failed", false},
		{"partial", "", "challenge", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := []zapcore.Field{zap.String("web_fetch_error_code", "crawler_failed"), zap.String("web_fetch_stage", "render"),
				zap.String("web_fetch_worker_stage", tc.stage), zap.String("web_fetch_worker_category", tc.category),
				zap.String("worker_category", "private-canary"), zap.String("url", "private-canary")}
			require.NoError(t, hook(entry, fields))
			message := receivedAlert(t, messages)
			require.Contains(t, message, `"error_code":"crawler_failed"`)
			require.Contains(t, message, `"stage":"render"`)
			require.NotContains(t, message, "canary")
			if tc.approved {
				require.Contains(t, message, `"worker_stage":"`+tc.stage+`"`)
				require.Contains(t, message, `"worker_category":"`+tc.category+`"`)
			} else {
				require.NotContains(t, message, "worker_")
			}
		})
	}
	called := false
	fields := []zapcore.Field{zap.String("web_fetch_error_code", "crawler_failed"), zap.String("web_fetch_stage", "render"),
		zap.Object("web_fetch_worker_category", untrustedFetchObject{called: &called}), zap.String("web_fetch_worker_stage", "render")}
	require.NoError(t, hook(entry, fields))
	require.False(t, called)
	require.NotContains(t, receivedAlert(t, messages), "worker_")
	fields = []zapcore.Field{zap.String("web_fetch_error_code", "crawler_failed"), zap.String("web_fetch_stage", "render"),
		zap.Namespace("private-canary"), zap.String("web_fetch_worker_stage", "render"), zap.String("web_fetch_worker_category", "challenge")}
	require.NoError(t, hook(entry, fields))
	require.NotContains(t, receivedAlert(t, messages), "worker_")
	fields = []zapcore.Field{zap.String("web_fetch_error_code", "crawler_failed"), zap.String("web_fetch_stage", "render"),
		zap.String("web_fetch_worker_stage", "render"), zap.String("web_fetch_worker_category", "challenge"), zap.String("web_fetch_worker_category", "challenge")}
	require.NoError(t, hook(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
	fields = []zapcore.Field{zap.String("web_fetch_error_code", "crawler_egress_unverified"), zap.String("web_fetch_stage", "verification"),
		zap.String("web_fetch_worker_stage", "render"), zap.String("web_fetch_worker_category", "challenge")}
	require.NoError(t, hook(entry, fields))
	require.NotContains(t, receivedAlert(t, messages), "worker_")
	entry.LoggerName = "graphql.other"
	require.NoError(t, hook(entry, fields))
	require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
}
