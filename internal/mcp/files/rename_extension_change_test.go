package files

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// renameOutboxJob is one queued RAG index job.
type renameOutboxJob struct {
	Project   string
	Path      string
	Operation string
}

// renameOutboxJobsAfter lists the user-namespace index jobs queued after id.
func renameOutboxJobsAfter(t *testing.T, svc *Service, apiKeyHash string, id int64) []renameOutboxJob {
	t.Helper()
	rows, err := svc.db.QueryContext(context.Background(), rebindSQL(`SELECT project, file_path, operation FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND system_owner = '' AND id > ? ORDER BY id`, svc.isPostgres), apiKeyHash, id)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var jobs []renameOutboxJob
	for rows.Next() {
		var job renameOutboxJob
		require.NoError(t, rows.Scan(&job.Project, &job.Path, &job.Operation))
		jobs = append(jobs, job)
	}
	require.NoError(t, rows.Err())
	return jobs
}

// lastRenameOutboxJobID returns the newest index job id, or zero.
func lastRenameOutboxJobID(t *testing.T, svc *Service) int64 {
	t.Helper()
	var id int64
	require.NoError(t, svc.db.QueryRowContext(context.Background(), "SELECT COALESCE(MAX(id), 0) FROM mcp_file_index_jobs").Scan(&id))
	return id
}

// TestRenameExtensionChangesFollowFilesystemRules verifies that renames which
// change the extension (.txt -> .md, .md -> .pdf, .md -> .txt) are allowed by
// filesystem rules alone, within a project and across projects. The bytes and
// the version history move unchanged, no system-owned state is written, and
// the only outbox work is one RAG DELETE of the old path and one UPSERT of the
// new path, which the worker then indexes.
func TestRenameExtensionChangesFollowFilesystemRules(t *testing.T) {
	for _, target := range []string{"alpha", "archive"} {
		for _, move := range []struct{ from, to string }{
			{"/notes/a.txt", "/notes/a.md"},
			{"/notes/b.md", "/notes/b.pdf"},
			{"/notes/c.md", "/notes/c.txt"},
		} {
			t.Run(target+"/"+path.Base(move.from)+"_to_"+path.Ext(move.to)[1:], func(t *testing.T) {
				svc, setClock := newSummaryTestService(t, &stubFileSummarizer{})
				ctx := context.Background()
				auth := crossProjectTestAuth()
				now := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
				setClock(now)
				mustCrossProjectWrite(t, svc, auth, "alpha", move.from, "# first draft")
				setClock(now.Add(time.Second))
				before := mustCrossProjectWrite(t, svc, auth, "alpha", move.from, "# final bytes")
				require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
				history, err := svc.ListVersions(ctx, auth, "alpha", move.from)
				require.NoError(t, err)
				require.NotEmpty(t, history, "the overwrite must have produced a retained version")
				lastJob := lastRenameOutboxJobID(t, svc)

				setClock(now.Add(2 * time.Second))
				result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: move.from, TargetProject: target, ToPath: move.to})
				require.NoError(t, err)
				require.Equal(t, 1, result.MovedCount)

				requireCrossProjectMissing(t, svc, auth, "alpha", move.from)
				requireCrossProjectContent(t, svc, auth, target, move.to, "# final bytes")
				stat, err := svc.Stat(ctx, auth, target, move.to)
				require.NoError(t, err)
				require.NotEqual(t, before, stat.Version, "a move advances the revision")
				moved, err := svc.ListVersions(ctx, auth, target, move.to)
				require.NoError(t, err)
				require.Len(t, moved, len(history), "history travels with the file")
				for i := range history {
					require.Equal(t, history[i].ID, moved[i].ID)
					require.Equal(t, history[i].Size, moved[i].Size)
				}
				left, err := svc.ListVersions(ctx, auth, "alpha", move.from)
				require.NoError(t, err)
				require.Empty(t, left, "no history stays at the old path")

				require.Equal(t, []renameOutboxJob{
					{Project: "alpha", Path: move.from, Operation: indexOperationDelete},
					{Project: target, Path: move.to, Operation: indexOperationUpsert},
				}, renameOutboxJobsAfter(t, svc, auth.APIKeyHash, lastJob))
				require.Zero(t, countCrossProjectRows(t, svc, `SELECT COUNT(1) FROM mcp_files WHERE system_owner <> ''`),
					"an ordinary rename writes no system-owned index state")

				require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
				requireIndexedRenameContent(t, svc, auth, target, move.to, "# final bytes")
				require.Zero(t, countCrossProjectRows(t, svc,
					`SELECT COUNT(1) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''`,
					auth.APIKeyHash, "alpha", move.from), "the old path keeps no searchable rows")
			})
		}
	}
}
