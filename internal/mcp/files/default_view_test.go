package files

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultViewDirectoryUnion pins the filesystem view: directories are the
// recursive union of both layers, and only a conflicting name is replaced by
// the project's entry. stat, read and list must agree.
func TestDefaultViewDirectoryUnion(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/notes/a.md", "default a")
	f.put(DefaultProject, "/notes/b.md", "default b")
	f.put(DefaultProject, "/notes/deep/c.md", "default c")
	f.put(DefaultProject, "/file/x.md", "default below a project file")
	f.put(DefaultProject, "/dir", "default file hidden by a project directory")

	// A same-name project directory merges with the default directory.
	f.put("p", "/notes/own.md", "own")
	require.Equal(t, []string{"DIRECTORY:/notes/deep", "FILE:/notes/a.md", "FILE:/notes/b.md", "FILE:/notes/own.md"}, f.listPaths("p", "/notes", 1))
	require.Equal(t, "default a", f.read("p", "/notes/a.md"))
	require.Equal(t, "own", f.read("p", "/notes/own.md"))

	// A project directory at a default file's name hides only that file; its
	// siblings stay visible.
	f.svc.mustWritePhysical(t, f.auth, "p", "/notes/a.md/x", "project dir at a default file's name")
	stat, err := f.svc.Stat(f.ctx, f.auth, "p", "/notes/a.md")
	require.NoError(t, err)
	require.Equal(t, FileTypeDirectory, stat.Type)
	_, err = f.svc.Read(f.ctx, f.auth, "p", "/notes/a.md", 0, -1)
	f.requireCode(err, ErrCodeIsDirectory)
	require.Equal(t, []string{"DIRECTORY:/notes/a.md", "DIRECTORY:/notes/deep", "FILE:/notes/b.md", "FILE:/notes/own.md"}, f.listPaths("p", "/notes", 1))
	require.Equal(t, "default a", f.read("q", "/notes/a.md"), "another project still sees the default")

	// A project file hides the whole default subtree at its name.
	f.svc.mustWritePhysical(t, f.auth, "p", "/file", "project file")
	stat, err = f.svc.Stat(f.ctx, f.auth, "p", "/file")
	require.NoError(t, err)
	require.Equal(t, FileTypeFile, stat.Type)
	stat, err = f.svc.Stat(f.ctx, f.auth, "p", "/file/x.md")
	require.NoError(t, err)
	require.False(t, stat.Exists)
	_, err = f.svc.List(f.ctx, f.auth, "p", "/file/x.md", 0, 10)
	f.requireCode(err, ErrCodeNotFound)

	// A project directory hides a default file of the same name. Through the
	// view, /dir is a visible file, so no new name can be created below it.
	_, err = f.svc.Write(f.ctx, f.auth, "p", "/dir/own.md", "own", "utf-8", 0, WriteModeTruncate)
	f.requireCode(err, ErrCodeNotDirectory)
	f.svc.mustWritePhysical(t, f.auth, "p", "/dir/own.md", "own")
	require.Equal(t, []string{"FILE:/dir/own.md"}, f.listPaths("p", "/dir", 1))
	stat, err = f.svc.Stat(f.ctx, f.auth, "p", "/dir")
	require.NoError(t, err)
	require.Equal(t, FileTypeDirectory, stat.Type)

	// The root lists both layers, merged by name.
	require.Equal(t, []string{"DIRECTORY:/dir", "DIRECTORY:/notes", "FILE:/file"}, f.listPaths("p", "", 1))
	require.Equal(t, []string{"DIRECTORY:/file", "DIRECTORY:/notes", "FILE:/dir"}, f.listPaths("q", "", 1))
}

// TestDefaultViewPathsAreCaseSensitiveAndSegmentAware covers SQLite, whose LIKE
// folds ASCII case: /Notes is not /notes, and /notes does not contain /notes2.
// Every prefix predicate of the view, the conflict checks and search agree.
func TestDefaultViewPathsAreCaseSensitiveAndSegmentAware(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.svc.embedder = errTestEmbedder{}
	f.put(DefaultProject, "/Notes/a.md", "needle upper default")
	f.put("p", "/notes2/b.md", "needle segment sibling")
	f.put("p", "/notes/c.md", "needle lower project")

	require.Equal(t, []string{"FILE:/notes/c.md"}, f.listPaths("p", "/notes", 1))
	require.Equal(t, []string{"FILE:/Notes/a.md"}, f.listPaths("p", "/Notes", 1))
	stat, err := f.svc.Stat(f.ctx, f.auth, "q", "/notes")
	require.NoError(t, err)
	require.False(t, stat.Exists, "a default directory is matched case-sensitively")

	// The physical single-project paths use the same predicates.
	_, err = f.svc.Write(f.ctx, f.auth, "p", "/NOTES", "a file, not the directory /notes", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = f.svc.Delete(f.ctx, f.auth, "p", "/Notes2", true)
	f.requireCode(err, ErrCodeNotFound)

	res, err := f.svc.Search(f.ctx, f.auth, "p", "needle", "/notes", 10)
	require.NoError(t, err)
	var hits []string
	for _, chunk := range res.Chunks {
		hits = append(hits, chunk.Project+":"+chunk.FilePath)
	}
	require.ElementsMatch(t, []string{"p:/notes2/b.md", "p:/notes/c.md"}, hits, "path_prefix is a literal, case-sensitive prefix")
}

// interleavingQueryer runs interleave once before the second statement of a
// resolution. A resolution that reads one consistent snapshot issues a single
// statement, so the interleaved writes can only land after it.
type interleavingQueryer struct {
	sqlQueryer
	statements int
	interleave func()
}

// QueryContext counts a statement and interleaves before the second one.
func (q *interleavingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.before()
	return q.sqlQueryer.QueryContext(ctx, query, args...)
}

// QueryRowContext counts a statement and interleaves before the second one.
func (q *interleavingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.before()
	return q.sqlQueryer.QueryRowContext(ctx, query, args...)
}

// before runs the interleaved writes exactly once, ahead of the second statement.
func (q *interleavingQueryer) before() {
	q.statements++
	if q.statements == 2 && q.interleave != nil {
		q.interleave()
		q.interleave = nil
	}
}

// TestDefaultViewReadResolvesOneSnapshot reproduces a mixed-generation read: an
// override appears and the default is then updated between the statements of
// one resolution. The result must equal the view at one instant: the old
// default before the change, or the override after it, never the new default
// the project never showed.
func TestDefaultViewReadResolvesOneSnapshot(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	before := f.put(DefaultProject, "/users.md", "default v1")
	q := &interleavingQueryer{sqlQueryer: f.svc.db, interleave: func() {
		f.svc.mustWritePhysical(t, f.auth, "p", "/users.md", "override")
		f.put(DefaultProject, "/users.md", "default v2")
	}}
	file, err := f.svc.resolveFile(f.ctx, q, f.auth.APIKeyHash, "p", "/users.md", true, true)
	require.NoError(t, err)
	require.Equal(t, before.Version, file.Version, "the read reflects one snapshot")
	require.Equal(t, "default v1", string(file.Content))
	require.Equal(t, 1, q.statements, "a read resolves with one statement")

	// The directory view of one path is one statement as well.
	f.put(DefaultProject, "/dir/a.md", "default")
	q = &interleavingQueryer{sqlQueryer: f.svc.db, interleave: func() {
		f.svc.mustWritePhysical(t, f.auth, "r", "/dir", "a project file now hides the subtree")
	}}
	visible, err := f.svc.visibleDescendants(f.ctx, q, f.auth.APIKeyHash, "r", "/dir", true)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Equal(t, DefaultProject, visible[0].Project)
	require.Equal(t, 1, q.statements)
}

// TestPhysicalProjectsContextNeverReachesDefaults covers automated owners of
// project-local state: with WithPhysicalProjects, a project addresses only its
// own rows, so maintenance can neither see nor change a shared default.
func TestPhysicalProjectsContextNeverReachesDefaults(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.svc.embedder = errTestEmbedder{}
	shared := f.put(DefaultProject, "/memory/s1/raw/old.md", "needle shared default")
	physical := WithPhysicalProjects(f.ctx)

	_, err := f.svc.Read(physical, f.auth, "p", "/memory/s1/raw/old.md", 0, -1)
	f.requireCode(err, ErrCodeNotFound)
	_, err = f.svc.List(physical, f.auth, "p", "/memory", 2, 10)
	f.requireCode(err, ErrCodeNotFound)
	_, err = f.svc.Delete(physical, f.auth, "p", "/memory/s1", true)
	f.requireCode(err, ErrCodeNotFound)
	res, err := f.svc.Search(physical, f.auth, "p", "needle", "", 10)
	require.NoError(t, err)
	require.Empty(t, res.Chunks)
	written, err := f.svc.Write(physical, f.auth, "p", "/memory/s1/raw/old.md", "project state", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, "p", written.ResolvedProject, "the write creates project state instead of editing the default")

	still, err := f.svc.Stat(f.ctx, f.auth, DefaultProject, "/memory/s1/raw/old.md")
	require.NoError(t, err)
	require.Equal(t, shared.Version, still.Version)
}
