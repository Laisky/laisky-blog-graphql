package files

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRenameOverwritePreservesReplacementSearch drains the real outbox and
// verifies a delayed cleanup cannot discard the active replacement.
func TestRenameOverwritePreservesReplacementSearch(t *testing.T) {
	for _, target := range []string{"alpha", "archive"} {
		t.Run(target, func(t *testing.T) {
			stub := &stubFileSummarizer{}
			svc, _, setClock := newSummaryTestService(t, stub)
			ctx := context.Background()
			auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
			now := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
			setClock(now)
			_, err := svc.Write(ctx, auth, "alpha", "/source.txt", "quasar source", "utf-8", 0, WriteModeTruncate)
			require.NoError(t, err)
			_, err = svc.Write(ctx, auth, target, "/destination.txt", "nebula destination", "utf-8", 0, WriteModeTruncate)
			require.NoError(t, err)
			worker := svc.NewIndexWorker()
			require.NoError(t, worker.RunOnce(ctx))
			setClock(now.Add(time.Second))
			_, err = svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/source.txt", TargetProject: target, ToPath: "/destination.txt", Overwrite: true})
			require.NoError(t, err)
			require.NoError(t, worker.RunOnce(ctx))
			replacement, err := svc.Search(ctx, auth, target, "quasar", "/destination.txt", 10)
			require.NoError(t, err)
			require.NotEmpty(t, replacement.Chunks)
			require.Contains(t, replacement.Chunks[0].ChunkContent, "quasar")
			require.Equal(t, "/destination.txt", replacement.Chunks[0].FilePath)
			requireIndexedRenameContent(t, svc, auth, target, "/destination.txt", "quasar source")
			later := now.Add(time.Second)
			require.NoError(t, svc.processDeleteJob(ctx, FileIndexJob{APIKeyHash: auth.APIKeyHash, Project: target, FilePath: "/destination.txt", FileUpdatedAt: &later}))
			replayed, err := svc.Search(ctx, auth, target, "quasar", "/destination.txt", 10)
			require.NoError(t, err)
			require.NotEmpty(t, replayed.Chunks)
			requireIndexedRenameContent(t, svc, auth, target, "/destination.txt", "quasar source")
			source, err := svc.Search(ctx, auth, "alpha", "quasar", "/source.txt", 10)
			require.NoError(t, err)
			require.Empty(t, source.Chunks)
		})
	}
}

// TestRenameOptOutNeverPublishesRAG ensures cleanup and delayed outbox jobs
// respect the stored indexing policy even at identical timestamps.
func TestRenameOptOutNeverPublishesRAG(t *testing.T) {
	for _, target := range []string{"alpha", "archive"} {
		t.Run(target, func(t *testing.T) {
			stub := &stubFileSummarizer{result: "This is a valid summary describing a document kept only in its own index."}
			svc, _, setClock := newSummaryTestService(t, stub)
			ctx := context.Background()
			auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
			now := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
			setClock(now)
			_, err := svc.Write(ctx, auth, target, "/destination.txt", "nebula destination", "utf-8", 0, WriteModeTruncate)
			require.NoError(t, err)
			worker := svc.NewIndexWorker()
			require.NoError(t, worker.RunOnce(ctx))
			_, err = svc.WriteWith(ctx, auth, "alpha", "/source.txt", "quasar source", "utf-8", 0, WriteModeTruncate, WriteOpts{SkipRAGIndex: true})
			require.NoError(t, err)
			wantSummary := "This document describes a local fixture for the native PageIndex search engine."
			hash := HashFileContent([]byte("quasar source"))
			published, err := svc.PublishPluginSummary(ctx, auth, "alpha", "/source.txt", PluginSummaryInput{
				ExpectedContentHash: hash, Summary: wantSummary, WordCount: SummaryWordCount(wantSummary),
				Status: SummaryStatusReady, Source: SummarySourcePageIndex, GenerationKey: hash,
			})
			require.NoError(t, err)
			require.True(t, published)
			beforeCalls := stub.callCount()
			_, err = svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/source.txt", TargetProject: target, ToPath: "/destination.txt", Overwrite: true})
			require.NoError(t, err)
			require.NoError(t, svc.processUpsertJob(ctx, FileIndexJob{APIKeyHash: auth.APIKeyHash, Project: target, FilePath: "/destination.txt", FileUpdatedAt: &now}))
			require.NoError(t, worker.RunOnce(ctx))
			require.Equal(t, beforeCalls, stub.callCount())
			var count int
			require.NoError(t, svc.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''", auth.APIKeyHash, target, "/destination.txt").Scan(&count))
			require.Zero(t, count)
			summary, _, _, _, _ := summaryRow(t, svc, auth.APIKeyHash, target, "/destination.txt")
			require.Equal(t, wantSummary, summary)
		})
	}
}

// requireIndexedRenameContent asserts persisted outbox output in addition to search,
// which can otherwise fall back to reading an unindexed active file on demand.
func requireIndexedRenameContent(t *testing.T, svc *Service, auth AuthContext, project, path, want string) {
	t.Helper()
	var content string
	require.NoError(t, svc.db.QueryRowContext(context.Background(),
		"SELECT file_content_hash FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''",
		auth.APIKeyHash, project, path).Scan(&content))
	require.Equal(t, HashFileContent([]byte(want)), content)
}
