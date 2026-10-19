package log

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

// adoptionBoundObject makes eager snapshot evaluation observable without external state.
type adoptionBoundObject struct {
	value string
	calls *atomic.Int64
}

// MarshalLogObject supplies only synthetic chosen object content.
func (object *adoptionBoundObject) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	object.calls.Add(1)
	encoder.AddString("chosen", object.value)
	return nil
}

// adoptionAlert joins the candidate SDK sender when a synthetic HTTP fixture ends.
func adoptionAlert(t *testing.T, endpoint string) *logSDK.Alert {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	alert, err := logSDK.NewAlert(ctx, endpoint, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"),
		logSDK.WithAlertMaxMessageBytes(1<<20))
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		alert.Close()
		select {
		case <-alert.Done():
		case <-time.After(3 * time.Second):
			t.Error("synthetic alert sender did not stop")
		}
	})
	return alert
}

// adoptionJSONFields compares standard JSON values without losing integer precision.
func adoptionJSONFields(t *testing.T, message string) map[string]any {
	t.Helper()
	offset := strings.LastIndex(message, "\n{")
	if offset >= 0 {
		message = message[offset+1:]
	}
	decoder := json.NewDecoder(strings.NewReader(message))
	decoder.UseNumber()
	var fields map[string]any
	require.NoError(t, decoder.Decode(&fields))
	return fields
}

// TestAlertAdoptionScopeCoreSnapshots preserves reviewed context without widening unrelated output.
func TestAlertAdoptionScopeCoreSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name string
		full bool
	}{
		{"graphql.laisky-graphql.mcp.web_search", true},
		{"graphql.laisky-graphql.mcp.web_fetch", true},
		{"graphql.laisky-graphql.mcp.web_fetch.fetch_dynamic_url_content", true},
		{"graphql.laisky-graphql.mcp.web_search_private", false},
		{"graphql.laisky-graphql.mcp.extract_key_info", false},
		{"graphql.laisky-graphql.other_operation", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, messages := alertReceiver(t)
			alert := adoptionAlert(t, endpoint)
			var local bytes.Buffer
			var calls atomic.Int64
			object := &adoptionBoundObject{value: "bound original", calls: &calls}
			binary := []byte{0, 255, 1}
			localCore := zapcore.NewCore(zapcore.NewJSONEncoder(zapcore.EncoderConfig{}), zapcore.AddSync(&local), zap.DebugLevel)
			logger := zap.New(zapcore.NewTee(localCore, WebDiagnosticAlertCore(alert))).Named(tc.name).
				With(zap.String("request_id", "0199c118-4229-7000-8000-000000000001")).
				With(zap.Namespace("context"), zap.Object("object", object), zap.Binary("binary", binary))
			require.EqualValues(t, 2, calls.Load(), "one eager evaluation per local and remote core")
			object.value, binary[0] = "mutated later", 99
			original := " \thttps://example.test/原文?q=chosen#fragment\n"
			logger.Error("chosen original diagnostic", zap.String("original_url", original), zap.Any("declared_output_markdown", nil))
			message := receivedAlert(t, messages)
			fields := adoptionJSONFields(t, message)
			localFields := adoptionJSONFields(t, strings.TrimSpace(local.String()))
			if tc.full {
				require.Equal(t, localFields, fields)
				require.Equal(t, "0199c118-4229-7000-8000-000000000001", fields["request_id"])
				nested, ok := fields["context"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, original, nested["original_url"])
				require.Equal(t, map[string]any{"chosen": "bound original"}, nested["object"])
				require.Equal(t, base64.StdEncoding.EncodeToString([]byte{0, 255, 1}), nested["binary"])
				require.Contains(t, nested, "declared_output_markdown")
				require.Nil(t, nested["declared_output_markdown"])
			} else {
				require.Empty(t, fields)
				require.NotEmpty(t, localFields, "local application logging remains complete")
			}
			require.EqualValues(t, 2, calls.Load(), "writes must not rerun bound serializers")
			require.Empty(t, messages, "one source entry must not enqueue duplicate alerts")
		})
	}
}

// TestAlertAdoptionLegacyHookCoreContracts preserves level gating and synchronous error returns.
func TestAlertAdoptionLegacyHookCoreContracts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		factory func(*logSDK.Alert) func(zapcore.Entry, []zapcore.Field) error
	}{
		{"fetch compatibility", WebFetchAlertHook},
		{"search compatibility", ToolFailureAlertHook},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, messages := alertReceiver(t)
			alert, err := logSDK.NewAlert(context.Background(), endpoint,
				logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"),
				logSDK.WithAlertMaxMessageBytes(256), logSDK.WithAlertFieldAllowlist())
			require.NoError(t, err)
			t.Cleanup(func() {
				alert.Close()
				select {
				case <-alert.Done():
				case <-time.After(3 * time.Second):
					t.Error("compatibility sender did not stop")
				}
			})
			hook := tc.factory(alert)
			oversize := strings.Repeat("x", 257)
			require.NoError(t, hook(zapcore.Entry{Level: zap.InfoLevel, Message: oversize}, nil),
				"disabled entries must remain ignored before size validation")
			require.Empty(t, messages)
			require.ErrorIs(t, hook(zapcore.Entry{Level: zap.ErrorLevel, Message: oversize}, nil), logSDK.ErrAlertMessageTooLarge)
			require.Empty(t, messages, "Write errors must be returned instead of silently rendered by CheckedEntry.Write")
			require.NoError(t, hook(zapcore.Entry{Level: zap.ErrorLevel, Message: "unchanged metadata operation"}, nil))
			require.True(t, strings.HasSuffix(receivedAlert(t, messages), "{}"))
			alert.Close()
			require.NoError(t, hook(zapcore.Entry{Level: zap.InfoLevel, Message: "disabled after close"}, nil))
			require.Error(t, hook(zapcore.Entry{Level: zap.ErrorLevel, Message: "enabled after close"}, nil))
			require.Empty(t, messages)
		})
	}
}
