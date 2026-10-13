package tools

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

type diagnosticSearchProvider struct {
	err       error
	nilOutput bool
	requestID string
}

func (p *diagnosticSearchProvider) Search(ctx context.Context, _ string) (*searchlib.SearchOutput, error) {
	p.requestID = diagnostics.RequestID(ctx)
	if p.err != nil || p.nilOutput {
		return nil, p.err
	}
	return &searchlib.SearchOutput{}, nil
}

func TestWebSearchDiagnosticsActualLogsAndAlert(t *testing.T) {
	for _, tt := range []struct {
		name, category, stage string
		err                   error
		nilOutput             bool
	}{
		{"http", "rate_limit", "response", diagnostics.HTTP(429), false},
		{"cancel", "canceled", "transport", diagnostics.Wrap(context.Canceled, "transport"), false},
		{"deadline", "deadline", "transport", diagnostics.Wrap(context.DeadlineExceeded, "transport"), false},
		{"dns", "dns", "transport", diagnostics.Wrap(&net.DNSError{Err: "private-dns-canary", Name: "private-host-canary"}, "transport"), false},
		{"tls", "tls", "transport", diagnostics.Wrap(x509.HostnameError{Host: "private-host-canary"}, "transport"), false},
		{"connection", "connection", "transport", diagnostics.Wrap(syscall.ECONNREFUSED, "transport"), false},
		{"auth", "auth", "response", diagnostics.HTTP(401), false},
		{"http_500", "provider_http", "response", diagnostics.HTTP(503), false},
		{"malformed", "malformed_response", "decode", diagnostics.Wrap(&json.SyntaxError{Offset: 1}, "decode"), false},
		{"unknown", "unknown", "provider", errors.New("private-error-canary"), false},
		{"nil", "internal", "provider", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			alert, err := logSDK.NewAlert(context.Background(), endpoint.URL, logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic"), appLog.ToolFailureAlertOption())
			require.NoError(t, err)
			t.Cleanup(alert.Close)
			var local bytes.Buffer
			logger, err := logSDK.NewWithName("request", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
				return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
			}), zap.HooksWithFields(appLog.ToolFailureAlertHook(alert)))
			require.NoError(t, err)
			provider := &diagnosticSearchProvider{err: tt.err, nilOutput: tt.nilOutput}
			var billingCalls int
			tool, err := NewWebSearchTool(provider, logger, func(context.Context) string { return "private-token-canary" }, func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil })
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("remote_addr", "private-address-canary"), zap.String("path", "private-user-path-canary"), zap.String("query", "private-query-canary"), zap.String("authorization", "private-auth-canary")))
			ctx = context.WithValue(ctx, ctxkeys.RequestID, "private-id-canary")
			result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "private-query-canary"}}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, 1, billingCalls)
			parsed, err := uuid.Parse(provider.requestID)
			require.NoError(t, err)
			require.NotEqual(t, uuid.Nil, parsed)
			require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "canary")
			require.NotContains(t, local.String(), "canary")
			decoder := json.NewDecoder(&local)
			var failure map[string]any
			for decoder.More() {
				var entry map[string]any
				require.NoError(t, decoder.Decode(&entry))
				if entry["msg"] == "web_search failed" {
					require.Nil(t, failure, "one terminal failure log")
					failure = entry
				}
			}
			require.NotNil(t, failure)
			require.Equal(t, "request.web_search", failure["logger"])
			require.Equal(t, tt.category, failure["web_search_category"])
			require.Equal(t, tt.stage, failure["web_search_stage"])
			require.Equal(t, provider.requestID, failure["web_search_request_id"])
			select {
			case message := <-messages:
				require.Contains(t, message, `"category":"`+tt.category+`"`)
				require.Contains(t, message, `"stage":"`+tt.stage+`"`)
				require.Contains(t, message, `"request_id":"`+provider.requestID+`"`)
				require.Contains(t, message, `"duration_ms":`)
				require.NotContains(t, message, "canary")
			case <-time.After(3 * time.Second):
				t.Fatal("actual synthetic alert not delivered")
			}
		})
	}
}

func TestWebSearchPreservesExistingLoggerName(t *testing.T) {
	var local bytes.Buffer
	name := "graphql.laisky-graphql.mcp.web_search"
	logger, err := logSDK.NewWithName(name, logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&local), zapcore.DebugLevel)
	}))
	require.NoError(t, err)
	tool, err := NewWebSearchTool(&stubSearchProvider{err: errors.New("synthetic")}, logger, func(context.Context) string { return "synthetic" }, func(context.Context, string, oneapi.Price, string) error { return nil })
	require.NoError(t, err)
	_, err = tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "synthetic"}}})
	require.NoError(t, err)
	decoder := json.NewDecoder(&local)
	found := false
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if entry["msg"] == "web_search failed" {
			found = true
			require.Equal(t, name, entry["logger"])
		}
	}
	require.True(t, found)
}
