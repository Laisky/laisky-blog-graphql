package search

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// addChallengeSignalWire uses the actual JSON decoder, including fields unknown
// to the pre-change consumer, so missing signal propagation has a compiling RED.
func addChallengeSignalWire(t *testing.T, task *rlibs.HTMLCrawlerTask, stage, category, signal string) {
	t.Helper()
	raw, err := task.ToString()
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &fields))
	fields["failure_stage"], fields["failure_category"], fields["failure_signal"] = stage, category, signal
	encoded, err := json.Marshal(fields)
	require.NoError(t, err)
	decoded, err := rlibs.NewHTMLCrawlerTaskFromString(string(encoded))
	require.NoError(t, err)
	*task = *decoded
}

// challengeSignalDiagnosticField observes the public diagnostic shape without
// referring to the new Go field before the regression's RED run.
func challengeSignalDiagnosticField(t *testing.T, diagnostic FetchFailureDiagnostic) string {
	t.Helper()
	raw, err := json.Marshal(diagnostic)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	signal, _ := fields["WorkerSignal"].(string)
	return signal
}

// TestWorkerChallengeSignalBoundedFailure accepts finite signals only on a
// failed render/challenge result bound to the exact submitted operation.
func TestWorkerChallengeSignalBoundedFailure(t *testing.T) {
	cases := []struct {
		name, stage, category, signal, expected string
		mutate                                  func(*rlibs.HTMLCrawlerTask)
	}{
		{"widget", "render", "challenge", "widget_element", "widget_element", nil},
		{"error", "render", "challenge", "error_element", "error_element", nil},
		{"platform", "render", "challenge", "challenge_platform_script", "challenge_platform_script", nil},
		{"options", "render", "challenge", "challenge_options_assignment", "challenge_options_assignment", nil},
		{"legacy", "render", "challenge", "", "", nil},
		{"unknown", "render", "challenge", "private-signal-canary https://private.invalid/?token=secret", "", nil},
		{"wrong stage", "conversion", "empty_body", "widget_element", "", nil},
		{"nonchallenge", "render", "render_failed", "widget_element", "", nil},
		{"wrong task", "render", "challenge", "widget_element", "", func(task *rlibs.HTMLCrawlerTask) { task.TaskID = "0199c118-4229-7000-8000-000000000099" }},
		{"wrong target", "render", "challenge", "widget_element", "", func(task *rlibs.HTMLCrawlerTask) { task.Url += "/other" }},
		{"wrong output", "render", "challenge", "widget_element", "", func(task *rlibs.HTMLCrawlerTask) { task.OutputMarkdown = false }},
		{"wrong policy", "render", "challenge", "widget_element", "", func(task *rlibs.HTMLCrawlerTask) { task.Egress.MaxRedirects++ }},
		{"missing policy", "render", "challenge", "widget_element", "", func(task *rlibs.HTMLCrawlerTask) { task.Egress = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
				task.Status = rlibs.TaskStatusFailed
				task.EgressReceipt = nil
				privateReason := "private-worker-reason-canary"
				task.FailedReason = &privateReason
				addChallengeSignalWire(t, task, tc.stage, tc.category, tc.signal)
				if tc.mutate != nil {
					tc.mutate(task)
				}
			}}
			body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "private-token-canary", true)
			require.Error(t, err)
			require.Empty(t, body)
			diagnostic := FetchDiagnostic(err)
			require.Equal(t, "crawler_failed", diagnostic.ErrorCode)
			require.Equal(t, "render", diagnostic.Stage)
			require.Equal(t, tc.expected, challengeSignalDiagnosticField(t, diagnostic))
			require.NotContains(t, err.Error(), "canary")
		})
	}
}

// TestWorkerChallengeSignalSuccessIsNotEvidence prevents stale fields from
// releasing a successful body without the existing connection receipt.
func TestWorkerChallengeSignalSuccessIsNotEvidence(t *testing.T) {
	for _, missingReceipt := range []bool{false, true} {
		store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
			addChallengeSignalWire(t, task, "render", "challenge", "widget_element")
			if missingReceipt {
				task.EgressReceipt = nil
			}
		}}
		body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "", true)
		if missingReceipt {
			require.Error(t, err)
			require.Empty(t, body)
			diagnostic := FetchDiagnostic(err)
			require.Equal(t, "crawler_egress_unverified", diagnostic.ErrorCode)
			require.Empty(t, challengeSignalDiagnosticField(t, diagnostic))
		} else {
			require.NoError(t, err)
			require.Equal(t, "safe markdown", string(body))
		}
	}
}

// TestFetchDiagnosticChallengeSignalSanitization checks the final projection
// even when an error producer directly constructs diagnostic metadata.
func TestFetchDiagnosticChallengeSignalSanitization(t *testing.T) {
	for _, tc := range []struct{ code, stage, task, workerStage, category, signal, expected string }{
		{"crawler_failed", "render", "0199c118-4229-7000-8000-000000000002", "render", "challenge", "widget_element", "widget_element"},
		{"crawler_failed", "render", "private-task-canary", "render", "challenge", "widget_element", ""},
		{"crawler_failed", "render", "", "render", "challenge", "widget_element", ""},
		{"crawler_failed", "render", "0199c118-4229-7000-8000-000000000002", "render", "timeout", "widget_element", ""},
		{"crawler_failed", "render", "0199c118-4229-7000-8000-000000000002", "conversion", "empty_body", "widget_element", ""},
		{"fetch_failed", "fetch", "0199c118-4229-7000-8000-000000000002", "render", "challenge", "widget_element", ""},
		{"crawler_failed", "render", "0199c118-4229-7000-8000-000000000002", "render", "challenge", "private-signal-canary", ""},
	} {
		raw, err := json.Marshal(map[string]string{"ErrorCode": tc.code, "Stage": tc.stage, "TaskID": tc.task, "WorkerStage": tc.workerStage, "WorkerCategory": tc.category, "WorkerSignal": tc.signal})
		require.NoError(t, err)
		var diagnostic FetchFailureDiagnostic
		require.NoError(t, json.Unmarshal(raw, &diagnostic))
		failure := errors.WithStack(&FetchFailureError{Diagnostic: diagnostic, Cause: errors.New("worker failed")})
		require.Equal(t, tc.expected, challengeSignalDiagnosticField(t, FetchDiagnostic(failure)))
	}
}
