package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// startupAdoptionObject makes eager per-core serialization observable.
type startupAdoptionObject struct {
	value string
	calls *atomic.Int32
}

// MarshalLogObject encodes only the synthetic chosen object.
func (object *startupAdoptionObject) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	object.calls.Add(1)
	encoder.AddString("chosen", object.value)
	return nil
}

// startupAdoptionFixture runs actual setupLogger with an owned loopback receiver.
func startupAdoptionFixture(t *testing.T) (*bytes.Buffer, *bytes.Buffer, <-chan string, *atomic.Int32) {
	t.Helper()
	messages := make(chan string, 4)
	hits := new(atomic.Int32)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body struct {
			Variables struct {
				Message string `json:"msg"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		hits.Add(1)
		messages <- body.Variables.Message
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprint(w, `{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`)
		if err != nil {
			t.Errorf("synthetic response write: %v", err)
		}
	}))
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for key, value := range map[string]string{
		"settings.logger.push_api": endpoint.URL, "settings.logger.alert_type": "synthetic",
		"settings.logger.push_token": "synthetic-transport-token", "log-level": "debug",
	} {
		previous := gconfig.Shared.GetString(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}
	previousDebug := gconfig.Shared.GetBool("debug")
	gconfig.Shared.Set("debug", false)
	t.Cleanup(func() { gconfig.Shared.Set("debug", previousDebug) })
	local, failures := new(bytes.Buffer), new(bytes.Buffer)
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(local), zap.DebugLevel)
	base, err := logSDK.NewWithName("graphql", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }), zap.ErrorOutput(zapcore.AddSync(failures)))
	require.NoError(t, err)
	original := appLog.Logger
	appLog.Logger = base
	t.Cleanup(func() { appLog.Logger = original })
	setupLogger(ctx)
	return local, failures, messages, hits
}

// startupAdoptionRecord extracts the existing metadata-framed field JSON.
func startupAdoptionRecord(t *testing.T, messages <-chan string) (string, map[string]any) {
	t.Helper()
	var message string
	select {
	case message = <-messages:
	case <-time.After(3 * time.Second):
		t.Fatal("actual startup alert missing")
	}
	offset := strings.Index(message, "\n{")
	require.GreaterOrEqual(t, offset, 0)
	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(message[offset+1:]), &record))
	return message, record
}

// startupAdoptionSingleDelivery rejects a second hook or duplicate enqueue.
func startupAdoptionSingleDelivery(t *testing.T, messages <-chan string, hits *atomic.Int32) {
	t.Helper()
	select {
	case <-messages:
		t.Fatal("one source entry produced duplicate alerts")
	case <-time.After(50 * time.Millisecond):
	}
	require.EqualValues(t, 1, hits.Load())
}

// TestStartupAlertCorePreservesSmallBoundContext isolates eager context from message budgets.
func TestStartupAlertCorePreservesSmallBoundContext(t *testing.T) {
	local, failures, messages, hits := startupAdoptionFixture(t)
	var calls atomic.Int32
	object := &startupAdoptionObject{value: "original bound object", calls: &calls}
	binary := []byte{0xff, 0, 0xfe, 'a'}
	expectedBinary := bytes.Clone(binary)
	target := " \thttps://example.invalid/原文?api_key=SYNTHETIC_CHOSEN_QUERY#fragment\n"
	// GraphQL startup does not choose a host field. This application context is
	// deliberately bound after setup, as real operation/request loggers are.
	logger := appLog.Logger.Named("mcp.web_fetch").
		With(zap.String("host", "synthetic-bound-host"), zap.String("request_id", "synthetic-request"),
			zap.Object("object", object), zap.Binary("bound_binary", binary)).
		With(zap.Namespace("request"), zap.String("url", target))
	require.EqualValues(t, 2, calls.Load(), "one eager encoding per local and alert core")
	object.value, binary[0] = "mutated later", 'X'
	logger.Error("synthetic small original failure", zap.String("unknown_chosen", "原文\nunknown-field"), zap.Any("declared_option", nil))
	message, record := startupAdoptionRecord(t, messages)
	require.Less(t, len(message), 16*1024, "this control must not depend on the larger SDK budget")
	require.Equal(t, "synthetic-bound-host", record["host"])
	require.Equal(t, "synthetic-request", record["request_id"])
	require.Equal(t, map[string]any{"chosen": "original bound object"}, record["object"])
	decoded, err := base64.StdEncoding.DecodeString(record["bound_binary"].(string))
	require.NoError(t, err)
	require.Equal(t, expectedBinary, decoded)
	nested, ok := record["request"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, target, nested["url"])
	require.Equal(t, "原文\nunknown-field", nested["unknown_chosen"])
	require.Contains(t, nested, "declared_option")
	require.Nil(t, nested["declared_option"])
	require.EqualValues(t, 2, calls.Load(), "Write must not evaluate bound object again")
	require.Contains(t, local.String(), "original bound object")
	require.NotContains(t, message, "synthetic-transport-token")
	require.Empty(t, failures.String())
	startupAdoptionSingleDelivery(t, messages, hits)
}

// TestStartupAlertCorePreservesLargeChosenInput proves actual startup overrides 16KiB.
func TestStartupAlertCorePreservesLargeChosenInput(t *testing.T) {
	local, failures, messages, hits := startupAdoptionFixture(t)
	html := "<body>" + strings.Repeat("original chosen HTML 原文\n", 1500) + "</body>"
	appLog.Logger.Named("mcp.web_fetch").Error("synthetic large original failure", zap.String("html", html))
	message, record := startupAdoptionRecord(t, messages)
	require.Greater(t, len(message), 16*1024)
	require.LessOrEqual(t, len(message), 1<<20)
	require.Equal(t, html, record["html"])
	require.Contains(t, local.String(), "original chosen HTML")
	require.Empty(t, failures.String())
	startupAdoptionSingleDelivery(t, messages, hits)
}

// TestStartupAlertCoreRejectsWholeOversizeRecord preserves the complete local record.
func TestStartupAlertCoreRejectsWholeOversizeRecord(t *testing.T) {
	local, failures, messages, hits := startupAdoptionFixture(t)
	html := strings.Repeat("x", 1<<20)
	appLog.Logger.Named("mcp.web_fetch").Error("synthetic oversize original failure", zap.String("html", html))
	require.Contains(t, failures.String(), logSDK.ErrAlertMessageTooLarge.Error())
	require.Contains(t, local.String(), html, "ordinary local logging must retain all chosen input")
	select {
	case <-messages:
		t.Fatal("oversize entry must not send a clipped record")
	case <-time.After(50 * time.Millisecond):
	}
	require.Zero(t, hits.Load())
}
