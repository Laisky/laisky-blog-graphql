package tools

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
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

func TestWebFetchFailureSafeCorrelation(t *testing.T) {
	const (
		chosenURL      = "https://1.1.1.1/private-url-canary?token=private-token-canary"
		upstreamCanary = "private-upstream-canary"
	)
	messages := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]json.RawMessage `json:"variables"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
			t.Errorf("decode synthetic request: %v", decodeErr)
			return
		}
		var message string
		if decodeErr := json.Unmarshal(body.Variables["msg"], &message); decodeErr != nil {
			t.Errorf("decode synthetic alert: %v", decodeErr)
			return
		}
		messages <- message
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"TelegramMonitorAlert":{"name":"synthetic"}}}`))
	}))
	t.Cleanup(endpoint.Close)
	alert, err := logSDK.NewAlert(context.Background(), endpoint.URL,
		logSDK.WithAlertType("synthetic"), logSDK.WithAlertToken("synthetic-token"))
	require.NoError(t, err)
	t.Cleanup(alert.Close)
	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("request", logSDK.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }), zap.HooksWithFields(alert.GetZapHook()))
	require.NoError(t, err)
	var billingCalls int
	tool := mustWebFetchTool(t, func(context.Context) string { return "synthetic-token" },
		func(context.Context, string, oneapi.Price, string) error { billingCalls++; return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{ErrorCode: "crawler_egress_unverified", Stage: "verification", TaskID: "0199c118-4229-7000-8000-000000000002"}, Cause: errors.New("renderer unavailable; response body: " + upstreamCanary)}
		})
	tool.logger = logger
	ctx := context.WithValue(context.Background(), ctxkeys.Logger, logger.With(zap.String("client", "private-client-canary")))
	ctx = context.WithValue(ctx, ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": chosenURL}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, billingCalls, "accepted billing is neither refunded nor repeated on provider failure")
	failures := observed.FilterMessage("web_fetch failed").All()
	require.Len(t, failures, 1, "the reviewed application logger receives the failure without inherited request fields")
	require.Equal(t, "fetch failed: crawler_egress_unverified", result.Content[0].(mcp.TextContent).Text)
	require.Equal(t, "request.web_fetch", failures[0].LoggerName)
	require.Equal(t, "0199c118-4229-7000-8000-000000000001", failures[0].ContextMap()["request_id"])
	require.Contains(t, failures[0].ContextMap(), "error", "local errors remain structured")
	require.NotContains(t, failures[0].ContextMap(), "client", "unselected request context must remain separate")
	localError, ok := failures[0].ContextMap()["error"].(string)
	require.True(t, ok)
	require.Contains(t, localError, "renderer unavailable")
	require.NotContains(t, localError, upstreamCanary)
	select {
	case message := <-messages:
		require.Contains(t, message, `"error_code":"crawler_egress_unverified"`)
		require.Contains(t, message, `"stage":"verification"`)
		require.Contains(t, message, `"request_id":"0199c118-4229-7000-8000-000000000001"`)
		require.Contains(t, message, `"task_id":"0199c118-4229-7000-8000-000000000002"`)
		require.Contains(t, message, `"duration_ms":`)
		require.Contains(t, message, `"caller_context_state":"active"`)
		require.Contains(t, message, `"error_lifecycle":"none"`)
		offset := strings.LastIndex(message, "\n{")
		require.GreaterOrEqual(t, offset, 0, "ordinary SDK fields must be present")
		var fields struct {
			OriginalURL       string `json:"original_url"`
			SubmittedURL      string `json:"submitted_url"`
			OriginalEncoding  string `json:"original_url_encoding"`
			SubmittedEncoding string `json:"submitted_url_encoding"`
			Error             string `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(message[offset+1:]), &fields))
		require.Equal(t, chosenURL, fields.OriginalURL)
		require.Equal(t, chosenURL, fields.SubmittedURL)
		require.Equal(t, "utf8", fields.OriginalEncoding)
		require.Equal(t, "utf8", fields.SubmittedEncoding)
		require.Contains(t, fields.Error, "renderer unavailable", "application error projection must retain useful context")
		require.NotContains(t, fields.Error, upstreamCanary)
		require.NotContains(t, message, upstreamCanary, "raw causes must not leak through rich-error companions")
		require.NotContains(t, message, "private-client-canary", "unselected context fields must remain absent")
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic alert was not delivered")
	}
}

func TestWebFetchUnknownFailureAndUntrustedCorrelation(t *testing.T) {
	for _, requestID := range []string{"", "private-id-canary", "https://1.1.1.1/private-id-canary", "00000000-0000-0000-0000-000000000000"} {
		t.Run(requestID, func(t *testing.T) {
			core, observed := observer.New(zapcore.DebugLevel)
			logger, err := logSDK.NewWithName("fallback", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			tool, err := NewWebFetchTool(&rlibs.DB{}, logger, func(context.Context) string { return "synthetic-token" },
				func(context.Context, string, oneapi.Price, string) error { return nil },
				func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
					return nil, errors.New("private-error-canary")
				})
			require.NoError(t, err)
			result, err := tool.Handle(context.WithValue(context.Background(), ctxkeys.RequestID, requestID), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1"}}})
			require.NoError(t, err)
			require.Equal(t, "fetch failed: fetch_failed", result.Content[0].(mcp.TextContent).Text)
			entries := observed.FilterMessage("web_fetch failed").All()
			require.Len(t, entries, 1)
			safeID := entries[0].ContextMap()["request_id"].(string)
			parsed, err := uuid.Parse(safeID)
			require.NoError(t, err)
			require.NotEqual(t, uuid.Nil, parsed)
			require.NotEqual(t, requestID, safeID)
		})
	}
}
