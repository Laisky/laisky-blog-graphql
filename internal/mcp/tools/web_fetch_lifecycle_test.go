package tools

import (
	"context"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// TestWebFetchCallerLifecycle separates caller state from error sentinels without changing tool results.
func TestWebFetchCallerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, caller, lifecycle, code string
		cause                         error
	}{
		{"active queue canceled", "active", "canceled", "fetch_canceled", context.Canceled},
		{"active queue deadline", "active", "deadline_exceeded", "fetch_timeout", context.DeadlineExceeded},
		{"active mixed", "active", "mixed", "fetch_canceled", errors.Join(context.Canceled, context.DeadlineExceeded)},
		{"active concrete and cancellation", "active", "mixed", "fetch_canceled", errors.Join(errors.New("synthetic queue error"), context.Canceled)},
		{"active nested concrete and deadline", "active", "mixed", "fetch_timeout", errors.Wrap(errors.Join(errors.New("synthetic queue error"), context.DeadlineExceeded), "result read")},
		{"caller canceled", "canceled", "canceled", "fetch_canceled", context.Canceled},
		{"caller deadline", "deadline_exceeded", "deadline_exceeded", "fetch_timeout", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.caller == "deadline_exceeded" {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer timeoutCancel()
			}
			ctx = context.WithValue(ctx, ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
			core, observed := observer.New(zapcore.InfoLevel)
			logger, err := logSDK.NewWithName("reviewed", logSDK.LevelInfo, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			calls := 0
			tool := mustWebFetchTool(t, func(context.Context) string { return "private-key-canary" },
				func(context.Context, string, oneapi.Price, string) error { return nil },
				func(fetchCtx context.Context, _ *rlibs.DB, _, _ string, _ bool) ([]byte, error) {
					calls++
					if tc.caller == "canceled" {
						cancel()
					}
					if tc.caller == "deadline_exceeded" {
						<-fetchCtx.Done()
					}
					return nil, &search.FetchFailureError{Diagnostic: search.FetchFailureDiagnostic{ErrorCode: "crawler_result_failed", Stage: "result", TaskID: "0199c118-4229-7000-8000-000000000002"}, Cause: tc.cause}
				})
			tool.logger = logger
			result, handleErr := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/private-target-canary"}}})
			require.NoError(t, handleErr)
			require.Equal(t, 1, calls)
			require.True(t, result.IsError)
			require.Equal(t, "fetch failed: "+tc.code, result.Content[0].(mcp.TextContent).Text)
			entries := observed.FilterMessage("web_fetch failed").All()
			require.Len(t, entries, 1)
			fields := entries[0].ContextMap()
			require.Equal(t, tc.caller, fields["caller_context_state"])
			require.Equal(t, tc.lifecycle, fields["error_lifecycle"])
			require.Equal(t, tc.code, fields["error_code"])
			require.Equal(t, "0199c118-4229-7000-8000-000000000002", fields["task_id"])
			require.NotContains(t, fields, "caller_receipt")
		})
	}
}

// TestWebFetchResponsePrepared labels only the constructed tool result, never client receipt.
func TestWebFetchResponsePrepared(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	logger, err := logSDK.NewWithName("reviewed", logSDK.LevelInfo, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	tool := mustWebFetchTool(t, func(context.Context) string { return "private-key-canary" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			return []byte("private-body-canary"), nil
		})
	tool.logger = logger
	ctx := context.WithValue(t.Context(), ctxkeys.RequestID, "0199c118-4229-7000-8000-000000000001")
	result, handleErr := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": "https://1.1.1.1/private-url-canary"}}})
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	entries := observed.FilterMessage("web_fetch response prepared").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, "response_prepared", fields["event"])
	require.Equal(t, "active", fields["caller_context_state"])
	require.Equal(t, "0199c118-4229-7000-8000-000000000001", fields["request_id"])
	require.Contains(t, fields, "duration_ms")
	require.NotContains(t, fields, "caller_receipt")
	require.Empty(t, observed.FilterMessage("web_fetch completed").All())
}
