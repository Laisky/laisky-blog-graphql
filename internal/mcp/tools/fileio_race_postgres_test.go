package tools

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileIORacePostgres verifies real advisory-lock cancellation and transaction
// rollback after the file update, snapshot, and hash update have already run.
func TestFileIORacePostgres(t *testing.T) {
	t.Run("cancel_waiter_and_independent_project", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		if !h.postgres {
			t.Skip("requires FILEIO_TEST_POSTGRES_DSN; no in-process-lock substitute")
		}
		fileIORaceOK(t, h.write(0, "/stable", "seed", "TRUNCATE", 0))
		locked := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- (files.DefaultLockProvider{}).WithProjectLock(h.clients[0].ctx, h.db, true, h.auth.APIKeyHash, h.project, time.Second, func(_ *sql.Tx) error {
				close(locked)
				select {
				case <-release:
					return nil
				case <-h.clients[0].ctx.Done():
					return h.clients[0].ctx.Err()
				}
			})
		}()
		defer func() {
			close(release)
			require.NoError(t, <-done)
		}()
		select {
		case <-locked:
		case <-h.clients[0].ctx.Done():
			t.Fatal("holder failed to acquire production lock")
		}
		// A different project must still progress while this project's lock is held.
		fileIORaceOK(t, h.call(2, "write", map[string]any{"project": "other", "path": "/other", "content": "ok"}))
		ctx, cancel := context.WithTimeout(h.clients[1].ctx, 100*time.Millisecond)
		defer cancel()
		h.clients[1].ctx = ctx
		r := h.write(1, "/stable", "must-not-commit", "TRUNCATE", 0)
		require.True(t, r.err != nil || r.failed, "canceled waiter reported success")
		h.assertStored(t, "/stable", "seed")
	})
	t.Run("rollback_after_file_mutation", func(t *testing.T) {
		h := newFileIORaceHarness(t, nil)
		if !h.postgres {
			t.Skip("requires FILEIO_TEST_POSTGRES_DSN for post-update fault injection")
		}
		fileIORaceOK(t, h.write(0, "/stable", "seed", "TRUNCATE", 0))
		_, err := h.db.ExecContext(h.clients[0].ctx, `CREATE FUNCTION fileio_reject_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fileio injected enqueue failure'; END $$`)
		require.NoError(t, err)
		_, err = h.db.ExecContext(h.clients[0].ctx, `CREATE TRIGGER fileio_reject_job BEFORE INSERT ON mcp_file_index_jobs FOR EACH ROW WHEN (NEW.file_path = '/stable') EXECUTE FUNCTION fileio_reject_job()`)
		require.NoError(t, err)
		r := h.write(1, "/stable", "replacement", "TRUNCATE", 0)
		require.NoError(t, r.err)
		require.True(t, r.failed, "injected post-update failure was ignored")
		h.assertStored(t, "/stable", "seed")
		versions, err := h.clients[0].svc.ListVersions(h.clients[0].ctx, h.auth, h.project, "/stable")
		require.NoError(t, err)
		require.Empty(t, versions, "snapshot escaped the rolled-back transaction")
		var count int
		require.NoError(t, h.db.QueryRowContext(h.clients[0].ctx,
			`SELECT COUNT(*) FROM mcp_file_index_jobs WHERE apikey_hash = $1 AND project = $2 AND file_path = $3 AND system_owner = $4`,
			h.auth.APIKeyHash, h.project, "/stable", "").Scan(&count))
		require.Equal(t, 1, count, "failed write left an extra index job")
	})
}
