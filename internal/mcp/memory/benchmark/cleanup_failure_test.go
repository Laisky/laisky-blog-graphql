package benchmark

import (
	"context"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestRunnerMarksCleanupFailureInvalid verifies a successful query phase cannot
// produce a valid baseline when the requested cleanup lifecycle fails.
func TestRunnerMarksCleanupFailureInvalid(t *testing.T) {
	t.Parallel()

	backend := &cleanupFailureBackend{}
	runner, err := NewRunner(backend, nil)
	require.NoError(t, err)

	dataset := Dataset{
		Name: "cleanup-failure",
		Version: "1",
		Documents: []Document{{ID: "doc", Path: "/fact.md", Content: "The code is ORBIT-17."}},
		Queries: []Query{{
			ID: "fact",
			Text: "What is the code?",
			GoldPaths: []string{"/fact.md"},
			GoldEvidence: []string{"ORBIT-17"},
		}},
	}
	report, err := runner.Run(context.Background(), dataset, RunConfig{
		Backend: "test",
		Plugin: "rag",
		Project: "cleanup-failure",
		TopK: 1,
		MinScore: 0.20,
		Concurrency: 1,
		Warmup: 0,
		Repetitions: 1,
		Seed: 42,
		IndexTimeout: time.Second,
		PollInterval: time.Millisecond,
		Cleanup: true,
	})
	require.NoError(t, err, "Runner returns diagnostic evidence; command-level validation rejects the report")
	require.Equal(t, ReportStatusInvalid, report.Status)
	require.Equal(t, 0, report.Quality.FailedQueries)
	require.Zero(t, report.Quality.ErrorRate)
	require.Len(t, report.ExecutionErrors, 1)
	require.ErrorContains(t, ValidateReport(report), "1 lifecycle operations failed")
}

type cleanupFailureBackend struct{}

func (*cleanupFailureBackend) Name() string { return "cleanup-failure" }

func (*cleanupFailureBackend) Write(context.Context, string, Document) error { return nil }

func (*cleanupFailureBackend) Search(context.Context, string, Query, int) ([]SearchHit, error) {
	return []SearchHit{{FilePath: "/fact.md", Content: "The code is ORBIT-17.", Score: 1}}, nil
}

func (*cleanupFailureBackend) Delete(context.Context, string, string, bool) error {
	return errors.New("forced cleanup failure")
}

func (*cleanupFailureBackend) Close(context.Context) error { return nil }
