package files

import (
	"context"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestRenameWithStateHookSharesTheTransaction verifies the system handle runs
// the ordinary rename and its derived-state hook as one transaction, for a
// same-project and a cross-project move: the hook sees the exact file changes,
// its writes commit with the move, and its error rolls back both.
func TestRenameWithStateHookSharesTheTransaction(t *testing.T) {
	for _, target := range []string{"alpha", "beta"} {
		t.Run(target, func(t *testing.T) {
			svc := newCrossProjectTestService(t, 10_000)
			auth := crossProjectTestAuth()
			ctx := context.Background()
			handle, err := svc.SystemNamespace("hooktest")
			require.NoError(t, err)
			atomicFS, ok := handle.(AtomicSystemFS)
			require.True(t, ok)

			mustCrossProjectWrite(t, svc, auth, "alpha", "/docs/a.md", "first")
			mustCrossProjectWrite(t, svc, auth, target, "/out/a.md", "replaced")
			req := RenameRequest{Project: "alpha", FromPath: "/docs/a.md", TargetProject: target, ToPath: "/out/a.md", Overwrite: true}

			_, err = atomicFS.RenameWithState(ctx, auth, req, func(ctx context.Context, change RenameStateChange, state SystemStateTx) error {
				require.NoError(t, state.Write(ctx, "state", "/marker", []byte("rolled back")))
				return errors.New("derived state storage failed")
			})
			require.ErrorContains(t, err, "derived state storage failed")
			requireCrossProjectContent(t, svc, auth, "alpha", "/docs/a.md", "first")
			requireCrossProjectContent(t, svc, auth, target, "/out/a.md", "replaced")
			_, err = handle.Read(ctx, "state", "/marker")
			require.True(t, IsCode(err, ErrCodeNotFound), "a failed hook leaves no state behind: %v", err)

			var seen RenameStateChange
			result, err := atomicFS.RenameWithState(ctx, auth, req, func(ctx context.Context, change RenameStateChange, state SystemStateTx) error {
				seen = change
				if err := state.Write(ctx, "state", "/marker", []byte("committed")); err != nil {
					return err
				}
				read, err := state.Read(ctx, "state", "/marker", "/absent")
				if err != nil {
					return err
				}
				require.Equal(t, map[string][]byte{"/marker": []byte("committed")}, read, "absent paths are omitted")
				return state.Delete(ctx, "state", "/absent")
			})
			require.NoError(t, err)
			require.Equal(t, 1, result.MovedCount)
			require.Len(t, seen.Mappings, 1)
			require.Equal(t, "/docs/a.md", seen.Mappings[0].OldPath)
			require.Equal(t, "/out/a.md", seen.Mappings[0].NewPath)
			require.Equal(t, HashFileContent([]byte("first")), seen.Mappings[0].ContentHash)
			require.Equal(t, []string{"/out/a.md"}, seen.OverwrittenPaths)
			requireCrossProjectMissing(t, svc, auth, "alpha", "/docs/a.md")
			requireCrossProjectContent(t, svc, auth, target, "/out/a.md", "first")
			marker, err := handle.Read(ctx, "state", "/marker")
			require.NoError(t, err)
			require.Equal(t, "committed", string(marker))

			_, err = atomicFS.RenameWithState(ctx, auth, req, nil)
			require.Error(t, err, "a nil hook is a programming error, not a silent plain rename")
		})
	}
}

// TestRenameWithPendingAndFailedRAGIndexing verifies that RAG indexing state
// never blocks a rename: a file whose index job is still pending and a file
// whose job failed permanently both move, within a project and across
// projects, including an extension change, and the outbox indexes them at the
// new path while the old path keeps no searchable rows.
func TestRenameWithPendingAndFailedRAGIndexing(t *testing.T) {
	for _, target := range []string{"alpha", "archive"} {
		t.Run(target, func(t *testing.T) {
			svc, _, setClock := newSummaryTestService(t, &stubFileSummarizer{})
			ctx := context.Background()
			auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
			now := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)
			setClock(now)
			_, err := svc.Write(ctx, auth, "alpha", "/pending.txt", "quasar pending", "utf-8", 0, WriteModeTruncate)
			require.NoError(t, err)
			_, err = svc.Write(ctx, auth, "alpha", "/failed.md", "pulsar failed", "utf-8", 0, WriteModeTruncate)
			require.NoError(t, err)
			_, err = svc.db.ExecContext(ctx, rebindSQL(`UPDATE mcp_file_index_jobs SET status = 'failed', retry_count = 9
				WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''`, svc.isPostgres), auth.APIKeyHash, "alpha", "/failed.md")
			require.NoError(t, err)

			setClock(now.Add(time.Second))
			for from, to := range map[string]string{"/pending.txt": "/moved/pending.md", "/failed.md": "/moved/failed.txt"} {
				result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: from, TargetProject: target, ToPath: to})
				require.NoError(t, err, from)
				require.Equal(t, 1, result.MovedCount)
				requireCrossProjectMissing(t, svc, auth, "alpha", from)
			}
			requireCrossProjectContent(t, svc, auth, target, "/moved/pending.md", "quasar pending")
			requireCrossProjectContent(t, svc, auth, target, "/moved/failed.txt", "pulsar failed")

			worker := svc.NewIndexWorker()
			require.NoError(t, worker.RunOnce(ctx))
			requireIndexedRenameContent(t, svc, auth, target, "/moved/pending.md", "quasar pending")
			requireIndexedRenameContent(t, svc, auth, target, "/moved/failed.txt", "pulsar failed")
			for _, old := range []string{"/pending.txt", "/failed.md"} {
				require.Zero(t, countCrossProjectRows(t, svc,
					`SELECT COUNT(1) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''`,
					auth.APIKeyHash, "alpha", old), old)
			}
			found, err := svc.Search(ctx, auth, target, "pulsar", "/moved", 10)
			require.NoError(t, err)
			require.NotEmpty(t, found.Chunks)
			for _, chunk := range found.Chunks {
				require.Contains(t, []string{"/moved/pending.md", "/moved/failed.txt"}, chunk.FilePath)
			}
		})
	}
}
