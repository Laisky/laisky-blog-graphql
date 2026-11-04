package log

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// TestCrawlerFailureContextMaximalDefaultHTTP proves long valid selected
// components and overrun counts survive the actual default SDK HTTP hook.
func TestCrawlerFailureContextMaximalDefaultHTTP(t *testing.T) {
	endpoint, messages := alertReceiver(t)
	alert, err := logSDK.NewAlert(context.Background(), endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-alert-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	mime := strings.Repeat("a", 63) + "/" + strings.Repeat("b", 63)
	count, status, redirect := int64(math.MaxInt64), int64(599), int64(20)
	worker := &crawleregress.FailureContext{TargetScheme: "https", TargetHostname: host, FinalScheme: "https", FinalHostname: host, ContentType: mime,
		UpstreamStatus: &status, RedirectCount: &redirect, RenderedHTMLBytes: &count, ConversionInputBytes: &count, ConversionOutputBytes: &count,
		ConversionOutputTrimmedBytes: &count, BodyTextRunes: &count, BodyTextNodes: &count, Converter: "html-to-markdown/v2"}
	require.NotNil(t, crawleregress.ReviewedFailureContext(worker))
	fields := WebFetchDiagnosticFields(WebFetchFailureSummary{ErrorCode: "crawler_failed", Stage: "render", WorkerStage: "conversion", WorkerCategory: "result_too_large",
		WorkerContext: worker, TargetScheme: "https", TargetHostname: host})
	fields = append(fields, zap.String("future_revision", "kept-unknown-field"))
	entry := zapcore.Entry{LoggerName: "graphql.laisky-graphql.mcp.web_fetch", Message: "web_fetch failed", Level: zapcore.ErrorLevel, Time: time.Now()}
	require.NoError(t, WebDiagnosticAlertHook(alert.GetZapHook())(entry, fields))
	message := receivedAlert(t, messages)
	for key, value := range map[string]any{"stage": "conversion", "caller_stage": "render", "target_hostname": host, "final_hostname": host, "content_type": mime,
		"rendered_html_bytes": count, "conversion_input_bytes": count, "conversion_output_bytes": count, "conversion_output_trimmed_bytes": count,
		"body_text_runes": count, "body_text_nodes": count, "future_revision": "kept-unknown-field"} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.Contains(t, message, fmt.Sprintf("%q:%s", key, encoded))
	}
}

// TestCrawlerFailureContextLegacyOmission keeps unknown worker observations
// absent instead of inventing zero/status/type for older worker payloads.
func TestCrawlerFailureContextLegacyOmission(t *testing.T) {
	fields := WebFetchDiagnosticFields(WebFetchFailureSummary{ErrorCode: "crawler_failed", Stage: "render", WorkerStage: "conversion", WorkerCategory: "empty_markdown"})
	keys := map[string]bool{}
	for _, field := range fields {
		keys[field.Key] = true
	}
	require.True(t, keys["caller_phase"])
	require.True(t, keys["effective_worker_stage"])
	for _, key := range []string{"upstream_status", "content_type", "redirect_count", "conversion_input_bytes", "conversion_output_bytes", "final_hostname"} {
		require.False(t, keys[key])
	}
}
