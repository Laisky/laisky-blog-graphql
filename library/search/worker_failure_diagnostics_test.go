package search

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

func addWorkerFailureWire(t *testing.T, task *rlibs.HTMLCrawlerTask, stage, category string) {
	t.Helper()
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	wire["failure_stage"], wire["failure_category"] = stage, category
	raw, err = json.Marshal(wire)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, task))
}

// TestWorkerFailureDiagnosticsBoundWire reproduces lost worker diagnostics on a failed result.
func TestWorkerFailureDiagnosticsBoundWire(t *testing.T) {
	for _, tc := range []struct {
		name, stage, category string
		mutate                func(*rlibs.HTMLCrawlerTask)
		approved              bool
	}{
		{"challenge", "render", "challenge", nil, true},
		{"conversion", "conversion", "empty_markdown", nil, true},
		{"policy", "admission", "policy_invalid", nil, true},
		{"verification", "verification", "evidence_rejected", nil, true},
		{"remote canceled", "render", "canceled", nil, true},
		{"remote timeout", "render", "timeout", nil, true},
		{"legacy", "", "", nil, false},
		{"legacy known reason", "", "", func(task *rlibs.HTMLCrawlerTask) { reason := "challenge"; task.FailedReason = &reason }, false},
		{"unknown category", "render", "private-category-canary", nil, false},
		{"wrong stage pair", "render", "conversion_failed", nil, false},
		{"unknown stage", "private-stage-canary", "challenge", nil, false},
		{"missing category", "render", "", nil, false},
		{"missing stage", "", "challenge", nil, false},
		{"mismatched task", "render", "challenge", func(task *rlibs.HTMLCrawlerTask) { task.TaskID = "0199c118-4229-7000-8000-000000000099" }, false},
		{"mismatched target", "render", "challenge", func(task *rlibs.HTMLCrawlerTask) { task.Url = "https://1.1.1.1/private-target-canary" }, false},
		{"mismatched output", "render", "challenge", func(task *rlibs.HTMLCrawlerTask) { task.OutputMarkdown = false }, false},
		{"missing policy", "render", "challenge", func(task *rlibs.HTMLCrawlerTask) { task.Egress = nil }, false},
		{"mismatched policy", "render", "challenge", func(task *rlibs.HTMLCrawlerTask) { task.Egress.MaxRedirects++ }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
				task.Status = rlibs.TaskStatusFailed
				private := "private-worker-reason-canary https://private.test/token"
				task.FailedReason = &private
				task.EgressReceipt = nil
				addWorkerFailureWire(t, task, tc.stage, tc.category)
				if tc.mutate != nil {
					tc.mutate(task)
				}
			}}
			body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "private-api-key-canary", true)
			require.Error(t, err)
			require.Empty(t, body)
			require.NotContains(t, err.Error(), "canary")
			require.NotErrorIs(t, err, context.Canceled, "remote enum is not a caller sentinel")
			require.NotErrorIs(t, err, context.DeadlineExceeded)
			diag := FetchDiagnostic(err)
			require.Equal(t, "crawler_failed", diag.ErrorCode)
			require.Equal(t, "render", diag.Stage, "consumer boundary stays backward-compatible")
			raw, marshalErr := json.Marshal(diag)
			require.NoError(t, marshalErr)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			if tc.approved {
				require.Equal(t, tc.stage, fields["WorkerStage"])
				require.Equal(t, tc.category, fields["WorkerCategory"])
			} else {
				require.Empty(t, fields["WorkerStage"])
				require.Empty(t, fields["WorkerCategory"])
			}
			require.NotContains(t, string(raw), "canary")
		})
	}
}

// TestWorkerFailureDiagnosticsDoNotChangeSuccess proves stale fields are not success evidence.
func TestWorkerFailureDiagnosticsDoNotChangeSuccess(t *testing.T) {
	store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
		addWorkerFailureWire(t, task, "render", "challenge")
	}}
	body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "", true)
	require.NoError(t, err)
	require.Equal(t, "safe markdown", string(body))
	store = &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
		addWorkerFailureWire(t, task, "render", "challenge")
		task.EgressReceipt = nil
	}}
	body, err = fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "", true)
	require.Empty(t, body)
	require.Error(t, err)
	require.Equal(t, "crawler_egress_unverified", FetchDiagnostic(err).ErrorCode)
}

// TestWorkerFailureSourceLogsAreContentFree covers source debug logs and retained queue causes.
func TestWorkerFailureSourceLogsAreContentFree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		store  *syntheticCrawlerStore
		target string
	}{
		{"submit", &syntheticCrawlerStore{submitErr: errors.WithStack(errors.New("private-queue-canary"))}, "https://1.1.1.1/private-target-canary?key=private-key-canary"},
		{"result", &syntheticCrawlerStore{resultErr: errors.WithStack(errors.New("private-result-canary"))}, "https://1.1.1.1/private-target-canary"},
		{"unknown status", &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) { task.Status = "private-worker-status-canary" }}, "https://1.1.1.1/doc"},
		{"admission", &syntheticCrawlerStore{}, "http://127.0.0.1/private-target-canary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger, err := logSDK.NewWithName("reviewed", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core {
				return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logs), zapcore.DebugLevel)
			}))
			require.NoError(t, err)
			ctx := gmw.SetLogger(context.Background(), logger.With(zap.String("authorization", "private-auth-canary")))
			ctx = WithFetchLogger(ctx, logger.With(zap.String("web_fetch_request_id", "0199c118-4229-7000-8000-000000000001")))
			body, err := fetchDynamicURLContent(ctx, tc.store, tc.target, "private-token-canary", true)
			require.Error(t, err)
			require.Empty(t, body)
			require.NotEmpty(t, logs.String())
			require.NotContains(t, logs.String(), "canary")
			if tc.store.submitErr != nil {
				require.ErrorIs(t, err, tc.store.submitErr)
			}
			if tc.store.resultErr != nil {
				require.ErrorIs(t, err, tc.store.resultErr)
			}
		})
	}
}

func TestWorkerFailureRequiresCanonicalSubmittedTaskID(t *testing.T) {
	store := &syntheticCrawlerStore{}
	_, err := store.AddHTMLCrawlerTaskWithEgress(context.Background(), "https://1.1.1.1/doc", "", true, &rlibs.CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1})
	require.NoError(t, err)
	store.task.Status = rlibs.TaskStatusFailed
	store.task.TaskID = "private-task-canary"
	addWorkerFailureWire(t, store.task, "render", "challenge")
	diag := FetchDiagnostic(failedCrawlerTask(store.task.TaskID, store.task.Url, *store.task.Egress, true, store.task))
	require.Empty(t, diag.TaskID)
	require.Empty(t, diag.WorkerStage)
	require.Empty(t, diag.WorkerCategory)
}
