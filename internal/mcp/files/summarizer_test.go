package files

import (
	"context"
	"strings"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestHierarchicalSummarizeSingleCall covers the fast path where the document fits
// within one call.
func TestHierarchicalSummarizeSingleCall(t *testing.T) {
	cfg := fileSummaryGenConfig{targetWords: 160, maxWords: 300, maxInputTokens: 100000, maxReduceCalls: 8, maxTotalInputTokens: 64000, maxTotalOutputTokens: 4096}
	calls := 0
	gen := func(_ context.Context, _, _ string, _ int) (string, error) {
		calls++
		return "final summary", nil
	}
	out, err := hierarchicalSummarize(context.Background(), cfg, "a short document", gen)
	require.NoError(t, err)
	require.Equal(t, "final summary", out)
	require.Equal(t, 1, calls)
}

// TestHierarchicalSummarizeMapReduce covers the map+reduce path for large inputs and
// asserts the call budget is respected (Q14).
func TestHierarchicalSummarizeMapReduce(t *testing.T) {
	cfg := fileSummaryGenConfig{targetWords: 160, maxWords: 300, maxInputTokens: 40, maxReduceCalls: 20, maxTotalInputTokens: 64000, maxTotalOutputTokens: 4096}
	calls := 0
	mapCalls := 0
	gen := func(_ context.Context, instructions, _ string, _ int) (string, error) {
		calls++
		switch {
		case strings.Contains(instructions, "summarizing part"):
			mapCalls++
			return "partial", nil
		case strings.Contains(instructions, "partial summaries"):
			return "reduced", nil
		default:
			return "final combined summary", nil
		}
	}
	doc := strings.Repeat("alpha beta gamma delta ", 20) // ~460 bytes -> a few segments
	out, err := hierarchicalSummarize(context.Background(), cfg, doc, gen)
	require.NoError(t, err)
	require.Equal(t, "final combined summary", out)
	require.Greater(t, mapCalls, 1, "large input must be split into map segments")
	require.LessOrEqual(t, calls, cfg.maxReduceCalls)
}

// TestHierarchicalSummarizeBudgetExhausted covers Q14: exceeding the per-file call
// budget yields errSummaryBudgetExhausted so the caller falls back deterministically.
func TestHierarchicalSummarizeBudgetExhausted(t *testing.T) {
	cfg := fileSummaryGenConfig{targetWords: 160, maxWords: 300, maxInputTokens: 10, maxReduceCalls: 1, maxTotalInputTokens: 64000, maxTotalOutputTokens: 4096}
	gen := func(_ context.Context, _, _ string, _ int) (string, error) { return "partial", nil }
	doc := strings.Repeat("alpha beta gamma delta ", 20)
	_, err := hierarchicalSummarize(context.Background(), cfg, doc, gen)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSummaryBudgetExhausted))
}

// TestHierarchicalSummarizeCancellation covers Q14/F05: cancellation stops further
// calls and returns promptly.
func TestHierarchicalSummarizeCancellation(t *testing.T) {
	cfg := fileSummaryGenConfig{targetWords: 160, maxWords: 300, maxInputTokens: 10, maxReduceCalls: 20, maxTotalInputTokens: 64000, maxTotalOutputTokens: 4096}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	gen := func(_ context.Context, _, _ string, _ int) (string, error) {
		calls++
		return "partial", nil
	}
	doc := strings.Repeat("alpha beta gamma delta ", 20)
	_, err := hierarchicalSummarize(ctx, cfg, doc, gen)
	require.Error(t, err)
	require.Equal(t, 0, calls, "no model call after cancellation")
}

// TestFileSummaryMigrationIdempotent covers Q01: running migrations twice is safe and
// adds all summary columns on SQLite.
func TestFileSummaryMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	require.NoError(t, RunMigrations(ctx, db, nil))
	require.NoError(t, RunMigrations(ctx, db, nil))

	for _, col := range []string{"content_hash", "file_summary", "summary_content_hash", "summary_status", "summary_source", "summary_word_count", "summary_updated_at"} {
		exists, err := sqliteColumnExists(ctx, db, "mcp_files", col)
		require.NoError(t, err)
		require.Truef(t, exists, "mcp_files.%s should exist", col)
	}
	exists, err := sqliteColumnExists(ctx, db, "mcp_file_chunks", "file_content_hash")
	require.NoError(t, err)
	require.True(t, exists)
	for _, col := range []string{"content_hash", "last_error_code", "summary_generation_key"} {
		exists, err := sqliteColumnExists(ctx, db, "mcp_file_index_jobs", col)
		require.NoError(t, err)
		require.Truef(t, exists, "mcp_file_index_jobs.%s should exist", col)
	}
}
