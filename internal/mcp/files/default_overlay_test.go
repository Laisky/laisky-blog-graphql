package files

import (
	"context"
	"database/sql"
	"maps"
	"sort"
	"strings"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// overlayFixture drives one tenant (and a foreign tenant) through the public
// Service API, plus physical probes that bypass the overlay.
type overlayFixture struct {
	t       *testing.T
	ctx     context.Context
	svc     *Service
	auth    AuthContext
	foreign AuthContext
}

// newOverlayFixture builds a SQLite-backed service with search enabled.
func newOverlayFixture(t *testing.T) *overlayFixture {
	t.Helper()
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 50
	settings.MaxProjectBytes = 1_000_000
	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	return &overlayFixture{
		t: t, ctx: context.Background(), svc: svc,
		auth:    AuthContext{APIKeyHash: "tenant-a", APIKey: "key-a", UserIdentity: "user:a"},
		foreign: AuthContext{APIKeyHash: "tenant-b", APIKey: "key-b", UserIdentity: "user:b"},
	}
}

// put blindly replaces a file through the addressed project and returns the result.
func (f *overlayFixture) put(project, path, content string) WriteResult {
	f.t.Helper()
	res, err := f.svc.Write(f.ctx, f.auth, project, path, content, "utf-8", 0, WriteModeTruncate)
	require.NoError(f.t, err, "write %s:%s", project, path)
	return res
}

// read returns the merged-view content of a path.
func (f *overlayFixture) read(project, path string) string {
	f.t.Helper()
	res, err := f.svc.Read(f.ctx, f.auth, project, path, 0, -1)
	require.NoError(f.t, err, "read %s:%s", project, path)
	return res.Content
}

// requireCode asserts a typed FileIO error code.
func (f *overlayFixture) requireCode(err error, code ErrorCode) {
	f.t.Helper()
	require.Error(f.t, err)
	require.True(f.t, IsCode(err, code), "want %s, got %v", code, err)
}

// physical reports whether one physical project stores a live row at path.
func (f *overlayFixture) physical(project, path string) bool {
	f.t.Helper()
	_, err := f.svc.findActiveFile(f.ctx, f.auth.APIKeyHash, project, path)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	require.NoError(f.t, err)
	return true
}

// listPaths returns sorted entry paths with their types.
func (f *overlayFixture) listPaths(project, path string, depth int) []string {
	f.t.Helper()
	res, err := f.svc.List(f.ctx, f.auth, project, path, depth, 100)
	require.NoError(f.t, err)
	out := make([]string, 0, len(res.Entries))
	for _, entry := range res.Entries {
		out = append(out, string(entry.Type)+":"+entry.Path)
	}
	sort.Strings(out)
	return out
}

// TestDefaultProjectNameIsReserved pins the only accepted name outside the charset.
func TestDefaultProjectNameIsReserved(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateProject(DefaultProject))
	require.True(t, IsDefaultProject("@DEFAULT"))
	for _, name := range []string{"@default", "@DEFAULTS", "@", "a@b", "@DEFAULT ", " @DEFAULT"} {
		require.Error(t, ValidateProject(name), name)
	}
	require.NoError(t, ValidateSearchProject(DefaultProject))
	require.NoError(t, ValidateProjectQuery("@def"))
	require.Error(t, ValidateProjectQuery("bad/query"))
}

// TestDefaultOverlayReadsMergedView covers the core overlay rule: the project
// file wins, a default shows through where the project has none, and the
// default layer itself and other tenants are unaffected.
func TestDefaultOverlayReadsMergedView(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/shared/users.md", "default users")
	defaultOnly := f.put(DefaultProject, "/shared/only-default.md", "default only")
	f.put(DefaultProject, "/root.txt", "root default")
	// Through the API a visible default is written in place, so the override is
	// set up physically (see the rename test for the public override recipe).
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/shared/users.md", "alpha users")
	f.put("alpha", "/alpha.txt", "alpha own")

	require.Equal(t, "alpha users", f.read("alpha", "/shared/users.md"))
	require.Equal(t, "default only", f.read("alpha", "/shared/only-default.md"))
	require.Equal(t, "default users", f.read("beta", "/shared/users.md"), "a project without its own file sees the default")
	require.Equal(t, "root default", f.read("never-written", "/root.txt"), "every project inherits defaults")

	stat, err := f.svc.Stat(f.ctx, f.auth, "alpha", "/shared/only-default.md")
	require.NoError(t, err)
	require.True(t, stat.Exists)
	require.Equal(t, FileTypeFile, stat.Type)
	require.Equal(t, defaultOnly.Version, stat.Version, "the token identifies the default row")
	stat, err = f.svc.Stat(f.ctx, f.auth, "beta", "/shared")
	require.NoError(t, err)
	require.Equal(t, FileTypeDirectory, stat.Type)
	root, err := f.svc.Stat(f.ctx, f.auth, "beta", "")
	require.NoError(t, err)
	require.False(t, root.UpdatedAt.IsZero(), "root of a project with only defaults reports their update time")

	require.Equal(t, []string{
		"DIRECTORY:/shared", "FILE:/alpha.txt", "FILE:/root.txt",
		"FILE:/shared/only-default.md", "FILE:/shared/users.md",
	}, f.listPaths("alpha", "", 2))
	listed, err := f.svc.List(f.ctx, f.auth, "alpha", "/shared/users.md", 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(len("alpha users")), listed.Entries[0].Size, "the shadowing file's metadata is listed")
	require.Equal(t, []string{"FILE:/shared/only-default.md", "FILE:/shared/users.md"}, f.listPaths("beta", "/shared", 1))

	// The default layer itself is physical: it never sees project files.
	require.Equal(t, []string{"DIRECTORY:/shared", "FILE:/root.txt"}, f.listPaths(DefaultProject, "", 1))
	_, err = f.svc.Read(f.ctx, f.auth, DefaultProject, "/alpha.txt", 0, -1)
	f.requireCode(err, ErrCodeNotFound)

	// Tenants stay isolated: another API key sees neither these defaults nor projects.
	_, err = f.svc.Read(f.ctx, f.foreign, "alpha", "/root.txt", 0, -1)
	f.requireCode(err, ErrCodeNotFound)
	foreignList, err := f.svc.List(f.ctx, f.foreign, "alpha", "", 3, 10)
	require.NoError(t, err)
	require.Empty(t, foreignList.Entries)
}

// TestDefaultOverlayShadowsWholeNames checks file/directory conflicts between
// layers: an upper file hides a lower subtree and an upper directory hides a
// lower file of the same name.
func TestDefaultOverlayShadowsWholeNames(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/a/b.txt", "lower under a")
	f.put(DefaultProject, "/x", "lower file x")
	// The merged namespace rejects these names, so a project can only hold them
	// when its files predate the conflicting defaults.
	_, err := f.svc.Write(f.ctx, f.auth, "proj", "/a", "upper file a", "utf-8", 0, WriteModeTruncate)
	f.requireCode(err, ErrCodeIsDirectory)
	_, err = f.svc.Write(f.ctx, f.auth, "proj", "/x/y.txt", "upper under x", "utf-8", 0, WriteModeTruncate)
	f.requireCode(err, ErrCodeNotDirectory)
	f.svc.mustWritePhysical(t, f.auth, "proj", "/a", "upper file a")
	f.svc.mustWritePhysical(t, f.auth, "proj", "/x/y.txt", "upper under x")

	_, err = f.svc.Read(f.ctx, f.auth, "proj", "/a/b.txt", 0, -1)
	f.requireCode(err, ErrCodeNotFound)
	stat, err := f.svc.Stat(f.ctx, f.auth, "proj", "/a")
	require.NoError(t, err)
	require.Equal(t, FileTypeFile, stat.Type)
	_, err = f.svc.List(f.ctx, f.auth, "proj", "/a", 1, 10)
	f.requireCode(err, ErrCodeNotFound)

	stat, err = f.svc.Stat(f.ctx, f.auth, "proj", "/x")
	require.NoError(t, err)
	require.Equal(t, FileTypeDirectory, stat.Type)
	_, err = f.svc.Read(f.ctx, f.auth, "proj", "/x", 0, -1)
	f.requireCode(err, ErrCodeIsDirectory)

	require.Equal(t, []string{"DIRECTORY:/x", "FILE:/a", "FILE:/x/y.txt"}, f.listPaths("proj", "", 3))
	require.Equal(t, []string{"DIRECTORY:/a", "FILE:/a/b.txt", "FILE:/x"}, f.listPaths("other", "", 3))
}

// TestDefaultOverlayWritesThroughToDefaults covers write routing: an existing
// default is changed globally, new names stay in the addressed project, and the
// merged namespace rejects file/directory conflicts.
func TestDefaultOverlayWritesThroughToDefaults(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/cfg.md", "v1")

	read, err := f.svc.Read(f.ctx, f.auth, "alpha", "/cfg.md", 0, -1)
	require.NoError(t, err)
	res, err := f.svc.WriteWith(f.ctx, f.auth, "alpha", "/cfg.md", "v2", "utf-8", 0, WriteModeTruncate, WriteOpts{ExpectedVersion: read.Version})
	require.NoError(t, err)
	require.Equal(t, DefaultProject, res.ResolvedProject)
	require.False(t, f.physical("alpha", "/cfg.md"), "write-through must not copy the default up")
	require.Equal(t, "v2", f.read("beta", "/cfg.md"), "a change to a default is global")
	require.Equal(t, "v2", f.read(DefaultProject, "/cfg.md"))

	_, err = f.svc.Write(f.ctx, f.auth, "beta", "/cfg.md", "+append", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.Equal(t, "v2+append", f.read("alpha", "/cfg.md"))

	// The token read before another project's change is stale everywhere.
	_, err = f.svc.WriteWith(f.ctx, f.auth, "alpha", "/cfg.md", "lost", "utf-8", 0, WriteModeTruncate, WriteOpts{ExpectedVersion: res.Version})
	f.requireCode(err, ErrCodeVersionConflict)
	_, err = f.svc.WriteWith(f.ctx, f.auth, "alpha", "/cfg.md", "dup", "utf-8", 0, WriteModeTruncate, WriteOpts{CreateOnly: true})
	f.requireCode(err, ErrCodeVersionConflict)

	created := f.put("alpha", "/new.md", "alpha only")
	require.Equal(t, "alpha", created.ResolvedProject)
	_, err = f.svc.Read(f.ctx, f.auth, "beta", "/new.md", 0, -1)
	f.requireCode(err, ErrCodeNotFound)

	// A project override is written in place and never touches the default.
	f.put(DefaultProject, "/pref.md", "default pref")
	_, err = f.svc.WriteWith(f.ctx, f.auth, "alpha", "/pref.md", "alpha pref", "utf-8", 0, WriteModeTruncate, WriteOpts{CreateOnly: true})
	f.requireCode(err, ErrCodeVersionConflict) // the default is visible, so the name is taken
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/pref.md", "alpha pref")
	override := f.put("alpha", "/pref.md", "alpha pref v2")
	require.Equal(t, "alpha", override.ResolvedProject)
	require.Equal(t, "default pref", f.read("beta", "/pref.md"))

	// Merged namespace: a default directory or default parent file blocks a name.
	f.put(DefaultProject, "/dir/inner.txt", "inner")
	_, err = f.svc.Write(f.ctx, f.auth, "alpha", "/dir", "file over dir", "utf-8", 0, WriteModeTruncate)
	f.requireCode(err, ErrCodeIsDirectory)
	f.put(DefaultProject, "/blocker", "file")
	_, err = f.svc.Write(f.ctx, f.auth, "alpha", "/blocker/child.txt", "x", "utf-8", 0, WriteModeTruncate)
	f.requireCode(err, ErrCodeNotDirectory)
	// Once the project owns the name as a directory, the default file is hidden.
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/blocker/own.txt", "own")
	f.put("alpha", "/blocker/child.txt", "now legal")
	require.Equal(t, "now legal", f.read("alpha", "/blocker/child.txt"))

	// Write-through enqueues RAG work for the physical default row.
	var jobs int
	require.NoError(t, f.svc.db.QueryRowContext(f.ctx,
		`SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ''`,
		f.auth.APIKeyHash, DefaultProject, "/cfg.md").Scan(&jobs))
	require.GreaterOrEqual(t, jobs, 3)
}

// mustWritePhysical creates a file in exactly one physical project, bypassing
// the overlay, to set up shadowing states that the public API cannot create.
func (s *Service) mustWritePhysical(t *testing.T, auth AuthContext, project, path, content string) {
	t.Helper()
	require.NoError(t, s.lockProvider.WithProjectLock(context.Background(), s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		_, err := s.writeWithinTx(context.Background(), tx, auth, project, path, []byte(content), WriteModeTruncate, 0, int64(len(content)), WriteOpts{})
		return err
	}))
}

// TestDefaultOverlayDeleteSemantics covers deleting through the merged view.
func TestDefaultOverlayDeleteSemantics(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)

	// Deleting a project override reveals the default again ("reset to default").
	f.put(DefaultProject, "/u.md", "default u")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/u.md", "alpha u")
	override, err := f.svc.Stat(f.ctx, f.auth, "alpha", "/u.md")
	require.NoError(t, err)
	defaultStat, err := f.svc.Stat(f.ctx, f.auth, "beta", "/u.md")
	require.NoError(t, err)
	ctx, err := WithFilePreconditions(f.ctx, f.auth, "alpha", "/u.md", FileOperationDelete, FilePreconditions{ExpectedVersion: defaultStat.Version})
	require.NoError(t, err)
	_, err = f.svc.Delete(ctx, f.auth, "alpha", "/u.md", false)
	f.requireCode(err, ErrCodeVersionConflict) // the hidden default is not what alpha sees
	ctx, err = WithFilePreconditions(f.ctx, f.auth, "alpha", "/u.md", FileOperationDelete, FilePreconditions{ExpectedVersion: override.Version})
	require.NoError(t, err)
	deleted, err := f.svc.Delete(ctx, f.auth, "alpha", "/u.md", false)
	require.NoError(t, err)
	require.Equal(t, 1, deleted.DeletedCount)
	require.Equal(t, "default u", f.read("alpha", "/u.md"))

	// Deleting a path served by the default deletes the default globally.
	ctx, err = WithFilePreconditions(f.ctx, f.auth, "alpha", "/u.md", FileOperationDelete, FilePreconditions{ExpectedVersion: defaultStat.Version})
	require.NoError(t, err)
	_, err = f.svc.Delete(ctx, f.auth, "alpha", "/u.md", false)
	require.NoError(t, err)
	_, err = f.svc.Read(f.ctx, f.auth, "beta", "/u.md", 0, -1)
	f.requireCode(err, ErrCodeNotFound)
	require.False(t, f.physical(DefaultProject, "/u.md"))

	// The deleted default keeps history that any project can list and restore.
	page, err := f.svc.ListVersionPage(f.ctx, f.auth, "beta", "/u.md", 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Versions)
	restoreID, err := ParseHistoryID(page.Versions[0].ID)
	require.NoError(t, err)
	snapshot, err := f.svc.ReadVersion(f.ctx, f.auth, "gamma", "/u.md", restoreID)
	require.NoError(t, err)
	require.Equal(t, "default u", string(snapshot.Content))
	restored, err := f.svc.RestoreVersion(f.ctx, f.auth, DefaultProject, "/u.md", restoreID)
	require.NoError(t, err)
	require.Equal(t, DefaultProject, restored.ResolvedProject)
	require.Equal(t, "default u", f.read("beta", "/u.md"))

	// Recursive delete removes every visible file in its own layer; a default
	// that a deleted project file hid becomes visible again.
	f.put(DefaultProject, "/d/a.txt", "default a")
	f.put(DefaultProject, "/d/b.txt", "default b")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/d/a.txt", "alpha a")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/d/c.txt", "alpha c")
	_, err = f.svc.Delete(f.ctx, f.auth, "alpha", "/d", false)
	f.requireCode(err, ErrCodeNotEmpty)
	deleted, err = f.svc.Delete(f.ctx, f.auth, "alpha", "/d", true)
	require.NoError(t, err)
	require.Equal(t, 3, deleted.DeletedCount)
	require.Equal(t, []string{"FILE:/d/a.txt"}, f.listPaths("alpha", "/d", 1))
	require.Equal(t, "default a", f.read("alpha", "/d/a.txt"))
	_, err = f.svc.Read(f.ctx, f.auth, "beta", "/d/b.txt", 0, -1)
	f.requireCode(err, ErrCodeNotFound)

	_, err = f.svc.Delete(f.ctx, f.auth, "alpha", "/missing", false)
	f.requireCode(err, ErrCodeNotFound)
}

// TestDefaultOverlayRenameSemantics covers renames whose source or destination
// is served by the default layer.
func TestDefaultOverlayRenameSemantics(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)

	// A default renamed through any project is renamed for every project, and its
	// history moves with it.
	f.put(DefaultProject, "/old.md", "default v1")
	f.put(DefaultProject, "/old.md", "default v2")
	moved, err := f.svc.Rename(f.ctx, f.auth, "alpha", "/old.md", "/new.md", false)
	require.NoError(t, err)
	require.Equal(t, 1, moved.MovedCount)
	require.Equal(t, "default v2", f.read("beta", "/new.md"))
	_, err = f.svc.Read(f.ctx, f.auth, "beta", "/old.md", 0, -1)
	f.requireCode(err, ErrCodeNotFound)
	require.True(t, f.physical(DefaultProject, "/new.md"))
	history, err := f.svc.ListVersionPage(f.ctx, f.auth, "beta", "/new.md", 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, history.Versions)

	// A project file renamed onto a visible default fails without overwrite, and
	// with overwrite only shadows it in that project.
	f.put("alpha", "/draft.md", "alpha draft")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/draft.md", "/new.md", false)
	f.requireCode(err, ErrCodeAlreadyExists)
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/draft.md", "/new.md", true)
	require.NoError(t, err)
	require.Equal(t, "alpha draft", f.read("alpha", "/new.md"))
	require.Equal(t, "default v2", f.read("beta", "/new.md"))
	require.True(t, f.physical("alpha", "/new.md"))

	// A default renamed over a project override replaces the visible override,
	// so every project sees the source. A default the override hides at the same
	// name was never visible, so its replacement is refused instead.
	f.put(DefaultProject, "/src.md", "default src")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/src.md", "/new.md", true)
	f.requireCode(err, ErrCodeAlreadyExists)
	require.Equal(t, "default v2", f.read("beta", "/new.md"), "the hidden default is untouched")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/fresh.md", "alpha draft")
	moved, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/src.md", "/fresh.md", true)
	require.NoError(t, err)
	require.Equal(t, 1, moved.MovedCount)
	require.Equal(t, "default src", f.read("alpha", "/fresh.md"))
	require.Equal(t, "default src", f.read("beta", "/fresh.md"))
	require.False(t, f.physical("alpha", "/fresh.md"))
	versions, err := f.svc.ListVersions(f.ctx, f.auth, "alpha", "/fresh.md")
	require.NoError(t, err)
	var sawOverride bool
	for _, version := range versions {
		snap, readErr := f.svc.ReadVersion(f.ctx, f.auth, "alpha", "/fresh.md", version.ID)
		require.NoError(t, readErr)
		sawOverride = sawOverride || string(snap.Content) == "alpha draft"
	}
	require.True(t, sawOverride, "a replaced destination keeps a history snapshot")

	// Destination parents are judged on the merged view and on the source layer.
	f.put(DefaultProject, "/parentfile", "file")
	f.put("alpha", "/mine.md", "mine")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/mine.md", "/parentfile/mine.md", false)
	f.requireCode(err, ErrCodeNotDirectory)
	f.put(DefaultProject, "/hidden", "default file hidden by alpha dir")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/hidden/own.txt", "own")
	f.put(DefaultProject, "/global.md", "global")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/global.md", "/hidden/global.md", false)
	f.requireCode(err, ErrCodeNotDirectory) // the default layer cannot nest under its own file
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/mine.md", "/hidden/mine.md", false)
	require.NoError(t, err, "a project file may move into the project's own directory")

	// Rename conditions compare the versions alpha actually sees.
	f.put(DefaultProject, "/cond.md", "default cond")
	stale, err := f.svc.Stat(f.ctx, f.auth, "alpha", "/cond.md")
	require.NoError(t, err)
	f.put("beta", "/cond.md", "beta changes the default")
	ctx, err := WithFilePreconditions(f.ctx, f.auth, "alpha", "/cond.md", FileOperationRename,
		FilePreconditions{ExpectedVersion: stale.Version, DestinationPath: "/cond2.md", DestinationMustNotExist: true})
	require.NoError(t, err)
	_, err = f.svc.Rename(ctx, f.auth, "alpha", "/cond.md", "/cond2.md", false)
	f.requireCode(err, ErrCodeVersionConflict)

	// A directory rename moves every visible file within its own layer.
	f.put(DefaultProject, "/tree/a.txt", "default a")
	f.put(DefaultProject, "/tree/b.txt", "default b")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/tree/a.txt", "alpha a")
	moved, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/tree", "/moved", false)
	require.NoError(t, err)
	require.Equal(t, 2, moved.MovedCount)
	require.Equal(t, "alpha a", f.read("alpha", "/moved/a.txt"))
	require.Equal(t, "default b", f.read("beta", "/moved/b.txt"))
	require.Equal(t, "default a", f.read("alpha", "/tree/a.txt"), "the hidden default stays at its name")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/moved", "/moved/inner", false)
	f.requireCode(err, ErrCodeInvalidPath)
}

// keywordEmbedder embeds a text as [1, 0] when it mentions "needle" and as
// [0, 1] otherwise, so semantic retrieval separates matches deterministically.
type keywordEmbedder struct{}

// EmbedTexts returns one keyword vector per input.
func (keywordEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for _, input := range inputs {
		if strings.Contains(input, "needle") {
			vectors = append(vectors, pgvector.NewVector([]float32{1, 0}))
		} else {
			vectors = append(vectors, pgvector.NewVector([]float32{0, 1}))
		}
	}
	return vectors, nil
}

// TestDefaultOverlaySearch covers physical retrieval: a project searches its
// own files and every default, including defaults its filesystem view hides,
// and every hit names the project that stores it.
func TestDefaultOverlaySearch(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.svc.embedder = errTestEmbedder{} // lexical retrieval first
	f.put(DefaultProject, "/u.md", "needle default override-target")
	f.put(DefaultProject, "/only.md", "needle default only")
	f.put(DefaultProject, "/sub/deep.md", "needle default deep")
	f.put(DefaultProject, "/dirname", "needle default file hidden by a project directory")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/u.md", "needle alpha override")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/sub", "alpha file hides the default subtree")
	f.svc.mustWritePhysical(t, f.auth, "alpha", "/dirname/own.md", "alpha directory at a default file's name")
	_, err := f.svc.Write(f.ctx, f.foreign, DefaultProject, "/foreign.md", "needle foreign tenant", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	hits := func(project string) map[string]string {
		t.Helper()
		res, err := f.svc.Search(f.ctx, f.auth, project, "needle", "", 20)
		require.NoError(t, err)
		out := make(map[string]string, len(res.Chunks))
		for _, chunk := range res.Chunks {
			out[chunk.Project+":"+chunk.FilePath] = chunk.ChunkContent
		}
		return out
	}
	defaults := map[string]string{
		"@DEFAULT:/u.md": "needle default override-target", "@DEFAULT:/only.md": "needle default only",
		"@DEFAULT:/sub/deep.md": "needle default deep", "@DEFAULT:/dirname": "needle default file hidden by a project directory",
	}
	alphaHits := maps.Clone(defaults)
	alphaHits["alpha:/u.md"] = "needle alpha override"

	require.Equal(t, alphaHits, hits("alpha"), "the raw-file fallback searches both physical projects")
	require.NoError(t, f.svc.NewIndexWorker().RunOnce(f.ctx))
	var defaultChunks int
	require.NoError(t, f.svc.db.QueryRowContext(f.ctx,
		`SELECT COUNT(1) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND system_owner = ''`,
		f.auth.APIKeyHash, DefaultProject).Scan(&defaultChunks))
	require.Positive(t, defaultChunks, "defaults are indexed once, under their own physical project")
	var copies int
	require.NoError(t, f.svc.db.QueryRowContext(f.ctx,
		`SELECT COUNT(1) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = '/only.md' AND system_owner = ''`,
		f.auth.APIKeyHash, "alpha").Scan(&copies))
	require.Zero(t, copies, "an inheriting project never gets its own index of a default")
	require.Equal(t, alphaHits, hits("alpha"), "indexed lexical retrieval keeps same-path and hidden documents")
	require.Equal(t, defaults, hits("beta"))

	// Each hit is readable from its physical source, also where alpha's view
	// shows a different file at the same path.
	read, err := f.svc.Read(f.ctx, f.auth, DefaultProject, "/u.md", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "needle default override-target", read.Content)
	require.Equal(t, "needle alpha override", f.read("alpha", "/u.md"))

	// Semantic retrieval (the in-memory vector path) has the same scope.
	semantic := newOverlayFixture(t)
	semantic.svc.embedder = keywordEmbedder{}
	semantic.svc.settings.Search.LexicalCandidates = 0
	semantic.put(DefaultProject, "/u.md", "needle default")
	semantic.svc.mustWritePhysical(t, semantic.auth, "alpha", "/u.md", "needle alpha")
	semantic.put("alpha", "/other.md", "unrelated words")
	require.NoError(t, semantic.svc.NewIndexWorker().RunOnce(semantic.ctx))
	res, err := semantic.svc.Search(semantic.ctx, semantic.auth, "alpha", "needle", "", 2)
	require.NoError(t, err)
	var semanticHits []string
	for _, chunk := range res.Chunks {
		semanticHits = append(semanticHits, chunk.Project+":"+chunk.FilePath)
	}
	require.ElementsMatch(t, []string{"alpha:/u.md", "@DEFAULT:/u.md"}, semanticHits)

	// Creating or deleting an override never changes which defaults are found.
	f.put("alpha", "/new-override.md", "needle temporary")
	_, err = f.svc.Rename(f.ctx, f.auth, "alpha", "/new-override.md", "/only.md", true)
	require.NoError(t, err)
	require.NoError(t, f.svc.NewIndexWorker().RunOnce(f.ctx))
	withOverride := hits("alpha")
	require.Equal(t, "needle default only", withOverride["@DEFAULT:/only.md"])
	require.Equal(t, "needle temporary", withOverride["alpha:/only.md"])
	ctx, err := WithFilePreconditions(f.ctx, f.auth, "alpha", "/only.md", FileOperationDelete, FilePreconditions{ExpectedVersion: f.put("alpha", "/only.md", "needle temporary").Version})
	require.NoError(t, err)
	_, err = f.svc.Delete(ctx, f.auth, "alpha", "/only.md", false)
	require.NoError(t, err)
	require.NoError(t, f.svc.NewIndexWorker().RunOnce(f.ctx))
	require.Equal(t, alphaHits, hits("alpha"), "after the override is deleted the default is still the same hit")

	require.Equal(t, map[string]string{
		"@DEFAULT:/u.md": "needle default override-target", "@DEFAULT:/only.md": "needle default only",
		"@DEFAULT:/sub/deep.md": "needle default deep", "@DEFAULT:/dirname": "needle default file hidden by a project directory",
		"alpha:/u.md": "needle alpha override",
	}, hits(ProjectWildcard), "the wildcard search stays physical and labels each project")
	physical := map[string]string{}
	for key, content := range hits(DefaultProject) {
		physical[key] = content
	}
	require.Len(t, physical, 4)
	for key := range physical {
		require.True(t, strings.HasPrefix(key, ":"), "a single-project search of the default layer is unlabeled: %s", key)
	}
}

// TestDefaultProjectDiscovery lists the default layer like any project with files.
func TestDefaultProjectDiscovery(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/x.md", "x")
	f.put("alpha", "/y.md", "y")

	all, err := f.svc.ListProjects(f.ctx, f.auth, "", "", 10)
	require.NoError(t, err)
	require.Equal(t, []string{DefaultProject, "alpha"}, all.Projects)
	found, err := f.svc.ListProjects(f.ctx, f.auth, "@def", "", 10)
	require.NoError(t, err)
	require.Equal(t, []string{DefaultProject}, found.Projects)
	after, err := f.svc.ListProjects(f.ctx, f.auth, "", DefaultProject, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, after.Projects)
}

// TestProjectLockOrderIsGlobal pins the deadlock-avoidance order shared by every
// multi-project mutation.
func TestProjectLockOrderIsGlobal(t *testing.T) {
	t.Parallel()
	forward := ProjectLockOrder("tenant", DefaultProject, "alpha", "", "alpha")
	backward := ProjectLockOrder("tenant", "alpha", DefaultProject)
	require.Equal(t, forward, backward)
	require.Len(t, forward, 2)
	require.LessOrEqual(t, hashLockKey("tenant", forward[0]), hashLockKey("tenant", forward[1]))
}

// TestRenameOntoOwnParentIsModeIndependent keeps overlay and physical renames
// in agreement: a file cannot replace the directory that contains it, whether
// or not a default touches the paths.
func TestRenameOntoOwnParentIsModeIndependent(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put("plain", "/p/a/b", "plain")
	_, err := f.svc.Rename(f.ctx, f.auth, "plain", "/p/a/b", "/p/a", false)
	f.requireCode(err, ErrCodeAlreadyExists)

	f.put(DefaultProject, "/q", "a default file the project hides")
	f.svc.mustWritePhysical(t, f.auth, "overlaid", "/q/a/b", "overlaid")
	_, err = f.svc.Rename(f.ctx, f.auth, "overlaid", "/q/a/b", "/q/a", false)
	f.requireCode(err, ErrCodeAlreadyExists)
}

// TestOverwritingRenameKeepsReplacedHistory reproduces a pre-existing loss: an
// overwriting rename discarded the replaced destination's last content instead
// of snapshotting it, in both physical and overlay mode.
func TestOverwritingRenameKeepsReplacedHistory(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put("plain", "/dst.md", "replaced content")
	f.put("plain", "/src.md", "moved content")
	_, err := f.svc.Rename(f.ctx, f.auth, "plain", "/src.md", "/dst.md", true)
	require.NoError(t, err)
	require.Equal(t, "moved content", f.read("plain", "/dst.md"))
	versions, err := f.svc.ListVersions(f.ctx, f.auth, "plain", "/dst.md")
	require.NoError(t, err)
	var contents []string
	for _, version := range versions {
		snapshot, readErr := f.svc.ReadVersion(f.ctx, f.auth, "plain", "/dst.md", version.ID)
		require.NoError(t, readErr)
		contents = append(contents, string(snapshot.Content))
	}
	require.Contains(t, contents, "replaced content")
}

// TestOverlayWriteChecksVersionBeforeNamespace keeps the physical error order:
// a stale condition on a directory name is VERSION_CONFLICT, not IS_DIRECTORY.
func TestOverlayWriteChecksVersionBeforeNamespace(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	token := f.put("plain", "/token.md", "x").Version
	f.put("plain", "/dir/child.md", "x")
	_, err := f.svc.WriteWith(f.ctx, f.auth, "plain", "/dir", "x", "utf-8", 0, WriteModeTruncate, WriteOpts{ExpectedVersion: token})
	f.requireCode(err, ErrCodeVersionConflict)

	f.put(DefaultProject, "/ddir/child.md", "x")
	_, err = f.svc.WriteWith(f.ctx, f.auth, "overlaid", "/ddir", "x", "utf-8", 0, WriteModeTruncate, WriteOpts{ExpectedVersion: token})
	f.requireCode(err, ErrCodeVersionConflict)
}
