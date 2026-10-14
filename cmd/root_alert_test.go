package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

// TestStartupAlertDiagnosticScope exercises the actual startup hook and HTTP serializer.
func TestStartupAlertDiagnosticScope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope      string
		diagnostic bool
	}{
		{"search", "mcp.web_search", true},
		{"fetch", "mcp.web_fetch", true},
		{"fetch child", "mcp.web_fetch.fetch_dynamic_url_content", true},
		{"unreviewed extract", "mcp.extract_key_info", false},
		{"scope lookalike", "mcp.web_search_private", false},
		{"unreviewed root", "other_operation", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := make(chan string, 4)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				var body struct {
					Variables struct {
						Message string `json:"msg"`
					} `json:"variables"`
				}
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
					http.Error(w, "invalid fixture", 400)
					return
				}
				messages <- body.Variables.Message
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"data":{"TelegramMonitorAlert":{"name":"fixture"}}}`)
			}))
			t.Cleanup(endpoint.Close)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			config := map[string]string{"settings.logger.push_api": endpoint.URL, "settings.logger.alert_type": "fixture", "settings.logger.push_token": "fixture-token", "log-level": "debug"}
			for key, value := range config {
				previous := gconfig.Shared.GetString(key)
				gconfig.Shared.Set(key, value)
				t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
			}
			previousDebug := gconfig.Shared.GetBool("debug")
			gconfig.Shared.Set("debug", false)
			t.Cleanup(func() { gconfig.Shared.Set("debug", previousDebug) })
			original := appLog.Logger
			t.Cleanup(func() { appLog.Logger = original })
			var local bytes.Buffer
			base, err := logSDK.NewWithName("graphql", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
				return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
			}))
			require.NoError(t, err)
			appLog.Logger = base
			setupLogger(ctx)
			logger := appLog.Logger.Named(tc.scope)
			if tc.diagnostic {
				logger.Error("synthetic diagnostic failed", zap.String("future_revision", "parser-v2"), zap.Int("attempt", 2), zap.Error(fmt.Errorf("navigation failed: unexpected EOF; api_key=private-key-canary; response body: private-body-canary")))
			} else {
				logger.With(zap.String("user_id", "private-user-canary")).Error("synthetic unrelated failure", zap.Error(fmt.Errorf("arbitrary unlabelled private-personal-content-canary")))
			}
			var message string
			select {
			case message = <-messages:
			case <-time.After(3 * time.Second):
				t.Fatal("actual startup alert missing")
			}
			require.Contains(t, message, tc.scope)
			require.NotContains(t, message, "canary")
			if tc.diagnostic {
				require.Contains(t, message, `"future_revision":"parser-v2"`)
				require.Contains(t, message, `"attempt":2`)
				require.Contains(t, message, "unexpected EOF")
				require.NotContains(t, message, "{}")
			} else {
				require.True(t, strings.HasSuffix(message, "{}"), "unreviewed app logs retain legacy metadata-only export")
			}
			select {
			case <-messages:
				t.Fatal("one source entry must not produce duplicate alerts")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}
