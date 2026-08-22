package files

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBackfillRAGSummariesIsBoundedAndResumable covers the operator-facing
// backfill contract: each page is durable, the cursor is exclusive, and a
// repeated run does not create duplicate work.
func TestBackfillRAGSummariesIsBoundedAndResumable(t *testing.T) {
	stub := &stubFileSummarizer{result: "A model summary after deterministic backfill."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	for _, path := range []string{"/a.txt", "/b.txt"} {
		_, err := svc.Write(ctx, auth, "proj", path, "alpha content for "+path, "utf-8", 0, WriteModeTruncate)
		require.NoError(t, err)
	}
	_, err := svc.db.ExecContext(ctx,
		`DELETE FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND system_owner = ''`,
		"hash", "proj")
	require.NoError(t, err)

	first, err := svc.BackfillRAGSummaries(ctx, SummaryBackfillOptions{
		APIKeyHash: "hash",
		Project:    "proj",
		BatchSize:  1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, first.Processed)
	require.Equal(t, 1, first.Enqueued)
	require.NotEmpty(t, first.NextPath)
	require.False(t, first.Done)

	second, err := svc.BackfillRAGSummaries(ctx, SummaryBackfillOptions{
		APIKeyHash: "hash",
		Project:    "proj",
		AfterPath:  first.NextPath,
		BatchSize:  1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, second.Processed)
	require.Equal(t, 1, second.Enqueued)

	repeated, err := svc.BackfillRAGSummaries(ctx, SummaryBackfillOptions{
		APIKeyHash: "hash",
		Project:    "proj",
		BatchSize:  10,
	})
	require.NoError(t, err)
	require.Equal(t, 0, repeated.Processed)
	require.True(t, repeated.Done)

	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	for _, path := range []string{"/a.txt", "/b.txt"} {
		_, status, contentHash, summaryHash, source := summaryRow(t, svc, "hash", "proj", path)
		require.Equal(t, string(SummaryStatusReady), status)
		require.Equal(t, contentHash, summaryHash)
		require.Equal(t, string(SummarySourceModel), source)
	}
}
