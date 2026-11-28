package files

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// history returns the snapshot contents ListVersions shows at project:path,
// newest first. Every entry must name physical, the project that stores it,
// ReadVersion must return the same snapshot, and ListVersionPage must list the
// same IDs in the same order.
func (f *overlayFixture) history(project, path, physical string) []string {
	f.t.Helper()
	versions, err := f.svc.ListVersions(f.ctx, f.auth, project, path)
	require.NoError(f.t, err)
	contents := make([]string, 0, len(versions))
	ids := make([]uint64, 0, len(versions))
	for _, version := range versions {
		require.Equal(f.t, physical, version.Project, "%s:%s lists a snapshot of %s", project, path, version.Project)
		snapshot, readErr := f.svc.ReadVersion(f.ctx, f.auth, project, path, version.ID)
		require.NoError(f.t, readErr)
		require.Equal(f.t, physical, snapshot.Project)
		contents = append(contents, string(snapshot.Content))
		ids = append(ids, version.ID)
	}
	require.Equal(f.t, ids, f.historyIDs(project, path), "ListVersions and ListVersionPage agree")
	return contents
}

// historyIDs returns every snapshot ID ListVersionPage shows at project:path,
// newest first, following the cursor one entry per page.
func (f *overlayFixture) historyIDs(project, path string) []uint64 {
	f.t.Helper()
	ids := []uint64{}
	var before uint64
	for range 100 {
		page, err := f.svc.ListVersionPage(f.ctx, f.auth, project, path, before, 1)
		require.NoError(f.t, err)
		for _, entry := range page.Versions {
			id, parseErr := ParseHistoryID(entry.ID)
			require.NoError(f.t, parseErr)
			ids = append(ids, id)
		}
		if !page.HasMore {
			require.Empty(f.t, page.NextCursor)
			return ids
		}
		require.Len(f.t, page.Versions, 1)
		require.Equal(f.t, page.Versions[0].ID, page.NextCursor)
		before = ids[len(ids)-1]
	}
	f.t.Fatalf("history of %s:%s did not end", project, path)
	return nil
}

// historyIDOf returns the ID of the one snapshot of project:path with content.
func (f *overlayFixture) historyIDOf(project, path, content string) uint64 {
	f.t.Helper()
	var found []uint64
	for _, id := range f.historyIDs(project, path) {
		snapshot, err := f.svc.ReadVersion(f.ctx, f.auth, project, path, id)
		require.NoError(f.t, err)
		if string(snapshot.Content) == content {
			found = append(found, id)
		}
	}
	require.Len(f.t, found, 1, "%s:%s has exactly one snapshot %q", project, path, content)
	return found[0]
}

// restore runs RestoreVersion with the caller's current-file conditions.
func (f *overlayFixture) restore(auth AuthContext, project, path string, id uint64, conditions FilePreconditions) (WriteResult, error) {
	f.t.Helper()
	ctx, err := WithFilePreconditions(f.ctx, auth, project, path, FileOperationRestore, conditions)
	require.NoError(f.t, err)
	return f.svc.RestoreVersion(ctx, auth, project, path, id)
}

// remove deletes the file project shows at path, conditioned on its version.
func (f *overlayFixture) remove(project, path string) {
	f.t.Helper()
	ctx, err := WithFilePreconditions(f.ctx, f.auth, project, path, FileOperationDelete, FilePreconditions{ExpectedVersion: f.version(project, path)})
	require.NoError(f.t, err)
	deleted, err := f.svc.Delete(ctx, f.auth, project, path, false)
	require.NoError(f.t, err)
	require.Equal(f.t, 1, deleted.DeletedCount)
}

// storageState renders every live and deleted file row, history row and index
// job of both tenants, so a refused call can prove it changed nothing.
func (f *overlayFixture) storageState() []string {
	f.t.Helper()
	var state []string
	collect := func(query string) {
		rows, err := f.svc.db.QueryContext(f.ctx, rebindSQL(query, f.svc.isPostgres))
		require.NoError(f.t, err)
		defer func() { require.NoError(f.t, rows.Close()) }()
		columns, err := rows.Columns()
		require.NoError(f.t, err)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(f.t, rows.Scan(pointers...))
			state = append(state, fmt.Sprint(values...))
		}
		require.NoError(f.t, rows.Err())
	}
	collect(`SELECT apikey_hash, project, path, deleted, incarnation_id || ':' || CAST(revision AS TEXT), size FROM mcp_files ORDER BY id`)
	collect(`SELECT id, apikey_hash, project, path, size FROM mcp_file_versions ORDER BY id`)
	collect(`SELECT id, apikey_hash, project, file_path, operation FROM mcp_file_index_jobs ORDER BY id`)
	return state
}

// runDefaultHistoryNamespaceScenario pins docs/proposals/fileio_default_project.md
// §6: history is the history of the file a path resolves to, never a union of
// the project and @DEFAULT. The two layers' snapshots of one path interleave
// by ID, so any union would mix them.
func runDefaultHistoryNamespaceScenario(t *testing.T, f *overlayFixture) {
	t.Helper()
	const path = "/a.md"
	f.put(DefaultProject, path, "default v1")
	f.svc.mustWritePhysical(t, f.auth, "alpha", path, "alpha v1")
	f.put(DefaultProject, path, "default v2")
	require.Equal(t, "alpha", f.put("alpha", path, "alpha v2").ResolvedProject, "alpha writes its own override")
	f.put(DefaultProject, path, "default v3")
	f.put("alpha", path, "alpha v3")

	// Each /a.md shows only its own history; beta resolves to the default.
	require.Equal(t, []string{"alpha v2", "alpha v1"}, f.history("alpha", path, "alpha"))
	require.Equal(t, []string{"default v2", "default v1"}, f.history(DefaultProject, path, DefaultProject))
	require.Equal(t, []string{"default v2", "default v1"}, f.history("beta", path, DefaultProject))
	alphaIDs, defaultIDs := f.historyIDs("alpha", path), f.historyIDs(DefaultProject, path)
	require.Less(t, defaultIDs[1], alphaIDs[1], "precondition: the layers' snapshot IDs interleave")
	require.Less(t, alphaIDs[1], defaultIDs[0])

	// Another layer's snapshot ID is not found, and a restore of it changes nothing.
	for _, id := range defaultIDs {
		_, err := f.svc.ReadVersion(f.ctx, f.auth, "alpha", path, id)
		f.requireCode(err, ErrCodeNotFound)
	}
	for _, id := range alphaIDs {
		for _, project := range []string{DefaultProject, "beta"} {
			_, err := f.svc.ReadVersion(f.ctx, f.auth, project, path, id)
			f.requireCode(err, ErrCodeNotFound)
		}
	}
	before := f.storageState()
	alphaVersion, defaultVersion := f.version("alpha", path), f.version(DefaultProject, path)
	_, err := f.restore(f.auth, "alpha", path, defaultIDs[0], FilePreconditions{ExpectedVersion: alphaVersion})
	f.requireCode(err, ErrCodeNotFound)
	_, err = f.restore(f.auth, "beta", path, alphaIDs[0], FilePreconditions{ExpectedVersion: defaultVersion})
	f.requireCode(err, ErrCodeNotFound)
	_, err = f.restore(f.auth, DefaultProject, path, alphaIDs[0], FilePreconditions{ExpectedVersion: defaultVersion})
	f.requireCode(err, ErrCodeNotFound)
	require.Equal(t, before, f.storageState(), "a refused restore has no side effects")

	// A layer's own snapshot restores into that layer only.
	restored, err := f.restore(f.auth, "alpha", path, alphaIDs[1], FilePreconditions{ExpectedVersion: alphaVersion})
	require.NoError(t, err)
	require.Equal(t, "alpha", restored.ResolvedProject)
	require.Equal(t, "alpha v1", f.read("alpha", path))
	require.Equal(t, "default v3", f.read("beta", path))
	require.Equal(t, []string{"alpha v3", "alpha v2", "alpha v1"}, f.history("alpha", path, "alpha"))
	restored, err = f.restore(f.auth, "beta", path, defaultIDs[1], FilePreconditions{ExpectedVersion: defaultVersion})
	require.NoError(t, err)
	require.Equal(t, DefaultProject, restored.ResolvedProject)
	require.Equal(t, "default v1", f.read(DefaultProject, path))
	require.Equal(t, "alpha v1", f.read("alpha", path))

	// Removing the override reveals the default and only the default's history.
	// alpha's own history is kept physically, but no longer addressed by the path.
	f.remove("alpha", path)
	require.Equal(t, "default v1", f.read("alpha", path))
	defaultHistory := f.history(DefaultProject, path, DefaultProject)
	require.Equal(t, []string{"default v3", "default v2", "default v1"}, defaultHistory)
	require.Equal(t, defaultHistory, f.history("alpha", path, DefaultProject))
	_, err = f.svc.ReadVersion(f.ctx, f.auth, "alpha", path, alphaIDs[0])
	f.requireCode(err, ErrCodeNotFound)
	kept, err := f.svc.ListVersions(WithPhysicalProjects(f.ctx), f.auth, "alpha", path)
	require.NoError(t, err)
	require.Len(t, kept, 4, "the override's history, including its deletion snapshot, stays in alpha")

	// Tenants stay isolated.
	for _, project := range []string{"alpha", "beta", DefaultProject} {
		foreign, listErr := f.svc.ListVersions(f.ctx, f.foreign, project, path)
		require.NoError(t, listErr)
		require.Empty(t, foreign)
		page, pageErr := f.svc.ListVersionPage(f.ctx, f.foreign, project, path, 0, 10)
		require.NoError(t, pageErr)
		require.Empty(t, page.Versions)
		_, readErr := f.svc.ReadVersion(f.ctx, f.foreign, project, path, defaultIDs[0])
		f.requireCode(readErr, ErrCodeNotFound)
	}
	before = f.storageState()
	_, err = f.restore(f.foreign, DefaultProject, path, defaultIDs[0], FilePreconditions{CreateOnly: true})
	f.requireCode(err, ErrCodeNotFound)
	require.Equal(t, before, f.storageState())
}

// runDefaultHistoryRecoveryScenario covers deleted files and pagination: a
// deleted path without a live file shows the addressed project's own history,
// never unrelated default history, and pages never mix the layers.
func runDefaultHistoryRecoveryScenario(t *testing.T, f *overlayFixture) {
	t.Helper()
	// A deleted project file is recoverable through its own project.
	f.put("alpha", "/own.md", "own v1")
	f.put("alpha", "/own.md", "own v2")
	f.remove("alpha", "/own.md")
	require.Equal(t, []string{"own v2", "own v1"}, f.history("alpha", "/own.md", "alpha"))
	require.Empty(t, f.history("beta", "/own.md", "beta"))
	restored, err := f.restore(f.auth, "alpha", "/own.md", f.historyIDOf("alpha", "/own.md", "own v1"), FilePreconditions{CreateOnly: true})
	require.NoError(t, err)
	require.Equal(t, "alpha", restored.ResolvedProject)
	require.Equal(t, "own v1", f.read("alpha", "/own.md"))
	require.False(t, f.physical(DefaultProject, "/own.md"))

	// A deleted default is recoverable through @DEFAULT only; a project that
	// never stored the path shows no history there.
	f.put(DefaultProject, "/gone.md", "gone v1")
	f.put(DefaultProject, "/gone.md", "gone v2")
	f.remove("beta", "/gone.md")
	require.False(t, f.physical(DefaultProject, "/gone.md"))
	require.Equal(t, []string{"gone v2", "gone v1"}, f.history(DefaultProject, "/gone.md", DefaultProject))
	require.Empty(t, f.history("beta", "/gone.md", "beta"))
	goneID := f.historyIDOf(DefaultProject, "/gone.md", "gone v1")
	_, err = f.svc.ReadVersion(f.ctx, f.auth, "beta", "/gone.md", goneID)
	f.requireCode(err, ErrCodeNotFound)
	before := f.storageState()
	_, err = f.restore(f.auth, "beta", "/gone.md", goneID, FilePreconditions{CreateOnly: true})
	f.requireCode(err, ErrCodeNotFound)
	require.Equal(t, before, f.storageState())
	restored, err = f.restore(f.auth, DefaultProject, "/gone.md", goneID, FilePreconditions{CreateOnly: true})
	require.NoError(t, err)
	require.Equal(t, DefaultProject, restored.ResolvedProject)
	require.Equal(t, "gone v1", f.read("beta", "/gone.md"))

	// A deleted override with its own history, next to a deleted default at the
	// same path: the project's own history, not the default's.
	f.put(DefaultProject, "/both.md", "both default v1")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/both.md", "both alpha v1")
	f.put(DefaultProject, "/both.md", "both default v2")
	f.remove("alpha", "/both.md")
	f.remove(DefaultProject, "/both.md")
	require.Equal(t, []string{"both alpha v1"}, f.history("alpha", "/both.md", "alpha"))
	require.Equal(t, []string{"both default v2", "both default v1"}, f.history(DefaultProject, "/both.md", DefaultProject))
	require.Empty(t, f.history("beta", "/both.md", "beta"))

	// Cursor pages of interleaved layers each walk one layer only.
	f.put(DefaultProject, "/p.md", "p default 0")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/p.md", "p alpha 0")
	for i := 1; i <= 3; i++ {
		f.put(DefaultProject, "/p.md", "p default "+strconv.Itoa(i))
		f.put("alpha", "/p.md", "p alpha "+strconv.Itoa(i))
	}
	alpha := f.history("alpha", "/p.md", "alpha")
	require.Equal(t, []string{"p alpha 2", "p alpha 1", "p alpha 0"}, alpha)
	require.Equal(t, []string{"p default 2", "p default 1", "p default 0"}, f.history("beta", "/p.md", DefaultProject))
	alphaIDs, defaultIDs := f.historyIDs("alpha", "/p.md"), f.historyIDs(DefaultProject, "/p.md")
	require.Less(t, alphaIDs[1], defaultIDs[0], "precondition: the layers' snapshot IDs interleave")
	require.Less(t, defaultIDs[0], alphaIDs[0])
	page, err := f.svc.ListVersionPage(f.ctx, f.auth, "alpha", "/p.md", defaultIDs[0], 200)
	require.NoError(t, err)
	require.Len(t, page.Versions, 2, "a cursor bounds IDs only and never pulls in another layer")
	require.Equal(t, strconv.FormatUint(alphaIDs[1], 10), page.Versions[0].ID)
	require.Equal(t, strconv.FormatUint(alphaIDs[2], 10), page.Versions[1].ID)
	page, err = f.svc.ListVersionPage(f.ctx, f.auth, "alpha", "/p.md", 0, 2)
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Equal(t, strconv.FormatUint(alphaIDs[1], 10), page.NextCursor)
}

// runDefaultHistoryReadConditionScenario covers a restore that reads history
// and writes in two steps (MCP, GraphQL and the HTTP editor): a read scoped to
// the restore checks the current-file conditions against the file the
// snapshot's namespace was resolved from, so a conditional write cannot later
// land a snapshot in another layer.
func runDefaultHistoryReadConditionScenario(t *testing.T, f *overlayFixture) {
	t.Helper()
	const path = "/c.md"
	f.put(DefaultProject, path, "c default v1")
	f.svc.mustWritePhysical(t, f.auth, "alpha", path, "c alpha v1")
	f.put("alpha", path, "c alpha v2")
	alphaID := f.historyIDOf("alpha", path, "c alpha v1")
	read := func(conditions FilePreconditions) error {
		ctx, err := WithFilePreconditions(f.ctx, f.auth, "alpha", path, FileOperationRestore, conditions)
		require.NoError(t, err)
		_, err = f.svc.ReadVersion(ctx, f.auth, "alpha", path, alphaID)
		return err
	}
	require.NoError(t, read(FilePreconditions{ExpectedVersion: f.version("alpha", path)}))
	f.requireCode(read(FilePreconditions{ExpectedVersion: f.version(DefaultProject, path)}), ErrCodeVersionConflict)
	f.requireCode(read(FilePreconditions{CreateOnly: true}), ErrCodeVersionConflict)

	// After the override is deleted the path resolves to the default, whose
	// history does not hold alpha's snapshot.
	f.remove("alpha", path)
	f.requireCode(read(FilePreconditions{ExpectedVersion: f.version(DefaultProject, path)}), ErrCodeNotFound)
}

// TestDefaultHistoryIsTheResolvedFileHistory runs the history namespace
// scenario on SQLite.
func TestDefaultHistoryIsTheResolvedFileHistory(t *testing.T) {
	t.Parallel()
	runDefaultHistoryNamespaceScenario(t, newOverlayFixture(t))
}

// TestDefaultHistoryRecoveryAndPagination runs the deleted-file and cursor
// scenario on SQLite.
func TestDefaultHistoryRecoveryAndPagination(t *testing.T) {
	t.Parallel()
	runDefaultHistoryRecoveryScenario(t, newOverlayFixture(t))
}

// TestDefaultHistoryRestoreReadChecksConditions runs the two-step restore
// scenario on SQLite.
func TestDefaultHistoryRestoreReadChecksConditions(t *testing.T) {
	t.Parallel()
	runDefaultHistoryReadConditionScenario(t, newOverlayFixture(t))
}
