package files_test

import (
	"context"
	"strings"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	pageindex "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
)

// TestFileIOConditionalLifecycle guards destructive operations and token ABA
// without treating file revisions as directory namespace snapshots.
func TestFileIOConditionalLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("delete_requires_current_file", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/a", "A", files.WriteModeTruncate, 0)
				base := readRaceVersion(t, f, "/a")
				f.write(t, 1, "/a", "B", files.WriteModeTruncate, 0)
				before := readRaceVersion(t, f, "/a")
				jobs, versions := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
				ctx := conditionRace(t, f, "/a", files.FileOperationDelete, files.FilePreconditions{ExpectedVersion: base.Version})
				_, err := f.svc[0].Delete(ctx, f.auth, "race", "/a", false)
				requireVersionConflict(t, err)
				require.Equal(t, before, readRaceVersion(t, f, "/a"))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				require.Equal(t, versions, f.count(t, "mcp_file_versions"))
				ctx = conditionRace(t, f, "/a", files.FileOperationDelete, files.FilePreconditions{ExpectedVersion: before.Version})
				result, err := f.svc[0].Delete(ctx, f.auth, "race", "/a", false)
				require.NoError(t, err)
				require.Equal(t, 1, result.DeletedCount)
				_, err = f.svc[1].Delete(ctx, f.auth, "race", "/a", false)
				requireVersionConflict(t, err)
			})

			t.Run("rename_checks_source_destination_and_noop", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/source", "source", files.WriteModeTruncate, 0)
				f.write(t, 0, "/target", "target", files.WriteModeTruncate, 0)
				source := readRaceVersion(t, f, "/source")
				target := readRaceVersion(t, f, "/target")
				p := files.FilePreconditions{ExpectedVersion: source.Version, DestinationPath: "/target", ExpectedDestinationVersion: target.Version}
				f.write(t, 1, "/target", "new target", files.WriteModeTruncate, 0)
				ctx := conditionRace(t, f, "/source", files.FileOperationRename, p)
				_, err := f.svc[0].Rename(ctx, f.auth, "race", "/source", "/target", true)
				requireVersionConflict(t, err)
				require.Equal(t, source, readRaceVersion(t, f, "/source"))
				f.assertStored(t, "/target", "new target")
				p.ExpectedDestinationVersion = readRaceVersion(t, f, "/target").Version
				ctx = conditionRace(t, f, "/source", files.FileOperationRename, p)
				moved, err := f.svc[0].Rename(ctx, f.auth, "race", "/source", "/target", true)
				require.NoError(t, err)
				require.Equal(t, 1, moved.MovedCount)
				after := readRaceVersion(t, f, "/target")
				require.Equal(t, "source", after.Content)
				require.Equal(t, strings.Split(source.Version, ":")[0]+":2", after.Version)
				// Moving away and back must not revive the source's old token.
				p = files.FilePreconditions{ExpectedVersion: after.Version, DestinationPath: "/source", DestinationMustNotExist: true}
				ctx = conditionRace(t, f, "/target", files.FileOperationRename, p)
				_, err = f.svc[0].Rename(ctx, f.auth, "race", "/target", "/source", false)
				require.NoError(t, err)
				back := readRaceVersion(t, f, "/source")
				require.NotEqual(t, source.Version, back.Version)
				ctx = conditionRace(t, f, "/source", files.FileOperationRename, files.FilePreconditions{ExpectedVersion: source.Version})
				_, err = f.svc[0].Rename(ctx, f.auth, "race", "/source", "/source", false)
				requireVersionConflict(t, err)
				ctx = conditionRace(t, f, "/source", files.FileOperationRename, files.FilePreconditions{ExpectedVersion: back.Version})
				noop, err := f.svc[0].Rename(ctx, f.auth, "race", "/source", "/source", false)
				require.NoError(t, err)
				require.Zero(t, noop.MovedCount)
				require.Equal(t, back, readRaceVersion(t, f, "/source"))
				// A protected source must never silently overwrite an unprotected target.
				_, err = f.svc[0].Rename(ctx, f.auth, "race", "/source", "/other", true)
				require.True(t, files.IsCode(err, files.ErrCodeInvalidArgument), "%v", err)
			})

			t.Run("restore_checks_live_revision_not_history_ID", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/a", "A", files.WriteModeTruncate, 0)
				old := readRaceVersion(t, f, "/a")
				f.write(t, 0, "/a", "B", files.WriteModeTruncate, 0)
				versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", "/a")
				require.NoError(t, err)
				require.Len(t, versions, 1)
				current := readRaceVersion(t, f, "/a")
				ctx := conditionRace(t, f, "/a", files.FileOperationRestore, files.FilePreconditions{ExpectedVersion: old.Version})
				_, err = f.svc[0].RestoreVersion(ctx, f.auth, "race", "/a", versions[0].ID)
				requireVersionConflict(t, err)
				require.Equal(t, current, readRaceVersion(t, f, "/a"))
				require.Equal(t, 1, f.count(t, "mcp_file_versions"))
				ctx = conditionRace(t, f, "/a", files.FileOperationRestore, files.FilePreconditions{ExpectedVersion: current.Version})
				restored, err := f.svc[0].RestoreVersion(ctx, f.auth, "race", "/a", versions[0].ID)
				require.NoError(t, err)
				require.Equal(t, restored.Version, readRaceVersion(t, f, "/a").Version)
				require.True(t, strings.HasSuffix(restored.Version, ":3"))
				f.assertStored(t, "/a", "A")
			})

			t.Run("file_tokens_never_authorize_directory_mutations", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/dir/a", "A", files.WriteModeTruncate, 0)
				child := readRaceVersion(t, f, "/dir/a")
				stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/dir")
				require.NoError(t, err)
				require.Empty(t, stat.Version)
				ctx := conditionRace(t, f, "/dir", files.FileOperationDelete, files.FilePreconditions{ExpectedVersion: child.Version})
				_, err = f.svc[0].Delete(ctx, f.auth, "race", "/dir", true)
				requireVersionConflict(t, err)
				ctx = conditionRace(t, f, "/dir", files.FileOperationRename, files.FilePreconditions{ExpectedVersion: child.Version})
				_, err = f.svc[0].Rename(ctx, f.auth, "race", "/dir", "/new", false)
				requireVersionConflict(t, err)
				f.assertStored(t, "/dir/a", "A")
			})

			t.Run("token_from_other_tenant_project_or_owner_does_not_match", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				f.write(t, 0, "/a", "user", files.WriteModeTruncate, 0)
				token := readRaceVersion(t, f, "/a").Version
				other := f.auth
				other.APIKeyHash = strings.Repeat("b", 64)
				for _, tc := range []struct {
					auth           files.AuthContext
					project, owner string
				}{
					{other, "race", ""}, {f.auth, "other-project", ""}, {f.auth, "system-race", "pageindex"},
				} {
					_, err := f.svc[1].WriteWith(f.ctx, tc.auth, tc.project, "/a", "unrelated", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{SystemOwner: tc.owner})
					require.NoError(t, err)
					_, err = f.svc[1].WriteWith(f.ctx, tc.auth, tc.project, "/a", "stale", "utf-8", 0, files.WriteModeTruncate,
						files.WriteOpts{SystemOwner: tc.owner, ExpectedVersion: token})
					requireVersionConflict(t, err)
				}
				f.assertStored(t, "/a", "user")
			})

			t.Run("pageindex_adapter_preserves_conditional_write", func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				fs, err := f.svc[0].SystemNamespace("pageindex")
				require.NoError(t, err)
				plugin, err := pageindex.New(pageindex.PluginDeps{UserFS: f.svc[0], SystemFS: fs})
				require.NoError(t, err)
				require.True(t, plugin.SupportsFileVersionPreconditions())
				f.write(t, 0, "/plain.txt", "before", files.WriteModeTruncate, 0)
				base := readRaceVersion(t, f, "/plain.txt")
				ctx := conditionRace(t, f, "/plain.txt", files.FileOperationWrite, files.FilePreconditions{ExpectedVersion: base.Version})
				// Non-long-document path uses deterministic summary publication, no LLM.
				written, err := plugin.Write(ctx, f.auth, "race", "/plain.txt", "after", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
				require.Equal(t, written.Version, readRaceVersion(t, f, "/plain.txt").Version)
				_, err = plugin.Write(ctx, f.auth, "race", "/plain.txt", "stale", "utf-8", 0, files.WriteModeTruncate)
				requireVersionConflict(t, err)
				f.assertStored(t, "/plain.txt", "after")
			})
		})
	}
}

// TestFileIOPostgresRevisionRollback proves revision changes share the actual
// transaction with file/history/outbox writes, rather than an external counter.
func TestFileIOPostgresRevisionRollback(t *testing.T) {
	f := newRaceFixture(t, "postgres", nil)
	f.write(t, 0, "/a", "old", files.WriteModeTruncate, 0)
	before := readRaceVersion(t, f, "/a")
	gate := &commitGate{prepared: make(chan struct{}), release: make(chan struct{}), abort: errors.New("injected rollback")}
	gated := f.service(t, 0, gate)
	t.Cleanup(gate.Release)
	done := make(chan error, 1)
	go func() {
		_, err := gated.WriteWith(f.ctx, f.auth, "race", "/a", "new", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: before.Version})
		done <- err
	}()
	select {
	case <-gate.prepared:
	case <-f.ctx.Done():
		t.Fatal("writer did not reach precommit gate")
	}
	require.Equal(t, before, readRaceVersion(t, f, "/a"))
	require.Zero(t, f.count(t, "mcp_file_versions"))
	require.Equal(t, 1, f.count(t, "mcp_file_index_jobs"))
	gate.Release()
	require.Error(t, <-done)
	require.Equal(t, before, readRaceVersion(t, f, "/a"))
	accepted, err := f.svc[1].WriteWith(f.ctx, f.auth, "race", "/a", "after rollback", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: before.Version})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(accepted.Version, ":2"))
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err = f.svc[1].WriteWith(ctx, f.auth, "race", "/a", "cancelled", "utf-8", 0, files.WriteModeTruncate, files.WriteOpts{ExpectedVersion: accepted.Version})
	require.Error(t, err)
	require.Equal(t, accepted.Version, readRaceVersion(t, f, "/a").Version)
}
