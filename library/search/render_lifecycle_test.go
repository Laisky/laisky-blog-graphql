package search

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Laisky/errors/v2"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestFetchResultLifecycle distinguishes reading a terminal value from accepting its body.
func TestFetchResultLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*rlibs.HTMLCrawlerTask)
		status   string
		verified bool
	}{
		{"verified", nil, "success", true},
		{"unverified success", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt = nil }, "success", false},
		{"empty success", func(task *rlibs.HTMLCrawlerTask) { task.ResultMarkdown = nil }, "success", false},
		{"failed", func(task *rlibs.HTMLCrawlerTask) { task.Status = rlibs.TaskStatusFailed }, "failed", false},
		{"unknown", func(task *rlibs.HTMLCrawlerTask) { task.Status = "private-status-canary" }, "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := logSDK.NewWithName("reviewed", logSDK.LevelInfo, zap.WrapCore(func(zapcore.Core) zapcore.Core {
				return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zapcore.InfoLevel)
			}))
			require.NoError(t, err)
			requestID := "0199c118-4229-7000-8000-000000000001"
			ctx := WithFetchLogger(t.Context(), logger.With(zap.String("request_id", requestID)))
			store := &syntheticCrawlerStore{mutate: tc.mutate}
			body, fetchErr := fetchDynamicURLContent(ctx, store, "https://1.1.1.1/private-target-canary?token=private-query-canary", "private-key-canary", true)
			require.Equal(t, tc.verified, fetchErr == nil)
			if !tc.verified {
				require.Empty(t, body)
			}
			var events []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				if line == "" {
					continue
				}
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				events = append(events, event)
			}
			observed, verified := 0, 0
			for _, event := range events {
				if event["event"] != "result_observed" && event["event"] != "result_verified" {
					continue
				}
				require.Equal(t, requestID, event["request_id"])
				require.Equal(t, store.task.TaskID, event["task_id"])
				require.Equal(t, "active", event["caller_context_state"])
				duration, ok := event["duration_ms"].(float64)
				require.True(t, ok)
				require.GreaterOrEqual(t, duration, float64(0))
				require.LessOrEqual(t, duration, float64(86400000))
				if event["event"] == "result_observed" {
					observed++
					require.Equal(t, tc.status, event["status"])
				} else {
					verified++
					require.Equal(t, 1, observed, "verification follows observation")
				}
			}
			require.Equal(t, 1, observed)
			if tc.verified {
				require.Equal(t, 1, verified)
			} else {
				require.Zero(t, verified)
			}
			require.NotContains(t, output.String(), "canary")
			require.NotContains(t, output.String(), "safe markdown")
			require.NotContains(t, output.String(), "caller_receipt")
		})
	}
}

// TestFetchLifecycleSentinelIdentity keeps legacy codes while exposing mixed causes without attribution.
func TestFetchLifecycleSentinelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, lifecycle, code string
		cause                 error
		canceled, deadline    bool
	}{
		{"concrete", "none", "crawler_result_failed", errors.New("synthetic queue error"), false, false},
		{"canceled", "canceled", "fetch_canceled", context.Canceled, true, false},
		{"deadline", "deadline_exceeded", "fetch_timeout", context.DeadlineExceeded, false, true},
		{"mixed", "mixed", "fetch_canceled", errors.Join(context.Canceled, context.DeadlineExceeded), true, true},
		{"concrete and cancellation", "mixed", "fetch_canceled", errors.Join(errors.New("synthetic queue error"), context.Canceled), true, false},
		{"nested concrete and deadline", "mixed", "fetch_timeout", errors.Wrap(errors.Join(errors.New("synthetic queue error"), errors.WithStack(context.DeadlineExceeded)), "result read"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fetchFailure(tc.cause, "crawler_result_failed", "result", "")
			require.Equal(t, tc.lifecycle, FetchErrorLifecycle(err))
			require.Equal(t, tc.code, FetchDiagnostic(err).ErrorCode)
			require.Equal(t, tc.canceled, errors.Is(err, context.Canceled))
			require.Equal(t, tc.deadline, errors.Is(err, context.DeadlineExceeded))
			require.ErrorIs(t, err, tc.cause)
			require.Equal(t, "active", FetchCallerContextState(t.Context()), "sentinels do not change caller state")
		})
	}
}

// TestFetchLifecycleTraversalBound reports an explicit unknown rather than guessing on deep chains.
func TestFetchLifecycleTraversalBound(t *testing.T) {
	var err error = context.Canceled
	for range 40 {
		err = errors.Wrap(err, "synthetic wrapper")
	}
	require.Equal(t, "unknown", FetchErrorLifecycle(err))
	require.ErrorIs(t, err, context.Canceled)
}
