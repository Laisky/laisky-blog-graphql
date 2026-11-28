package files

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingLockProvider records each tenant admission and delegates to the database.
type recordingLockProvider struct {
	DefaultLockProvider
	mu      sync.Mutex
	tenants []string
}

func (p *recordingLockProvider) WithTenantLock(ctx context.Context, db *sql.DB, pg bool, tenant string, fn func(*sql.Tx) error) error {
	p.mu.Lock()
	p.tenants = append(p.tenants, tenant)
	p.mu.Unlock()
	return p.DefaultLockProvider.WithTenantLock(ctx, db, pg, tenant, fn)
}

func (p *recordingLockProvider) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.tenants
	p.tenants = nil
	return out
}

// newCrossProjectOverlayFixture returns an overlay fixture whose lock scopes are recorded.
func newCrossProjectOverlayFixture(t *testing.T) (*overlayFixture, *recordingLockProvider) {
	t.Helper()
	f := newOverlayFixture(t)
	locks := &recordingLockProvider{}
	f.svc.lockProvider = locks
	return f, locks
}

// move runs a conditioned cross-project move of one file.
func (f *overlayFixture) move(project, from, target, to string, overwrite bool, conditions FilePreconditions) (RenameResult, error) {
	f.t.Helper()
	conditions.DestinationPath, conditions.DestinationProject = to, target
	ctx, err := WithFilePreconditions(f.ctx, f.auth, project, from, FileOperationRename, conditions)
	require.NoError(f.t, err)
	return f.svc.Rename(ctx, f.auth, RenameRequest{Project: project, FromPath: from, TargetProject: target, ToPath: to, Overwrite: overwrite})
}

// version returns the version project shows at path.
func (f *overlayFixture) version(project, path string) string {
	f.t.Helper()
	stat, err := f.svc.Stat(f.ctx, f.auth, project, path)
	require.NoError(f.t, err)
	require.True(f.t, stat.Exists, "%s:%s", project, path)
	return stat.Version
}

// indexJobs lists the pending index jobs of one path as "OPERATION project".
func (f *overlayFixture) indexJobs(path string) []string {
	f.t.Helper()
	rows, err := f.svc.db.QueryContext(f.ctx,
		`SELECT operation, project FROM mcp_file_index_jobs WHERE apikey_hash = ? AND file_path = ? AND system_owner = '' AND status = 'pending' ORDER BY id`,
		f.auth.APIKeyHash, path)
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, rows.Close()) }()
	var jobs []string
	for rows.Next() {
		var operation, project string
		require.NoError(f.t, rows.Scan(&operation, &project))
		jobs = append(jobs, operation+" "+project)
	}
	require.NoError(f.t, rows.Err())
	return jobs
}

// TestDefaultCrossProjectMoves is the combined #106/#107 lifecycle: the source
// is the physical file a normal read resolves, the destination is the explicit
// physical target, and an inherited source moves instead of being copied.
func TestDefaultCrossProjectMoves(t *testing.T) {
	t.Parallel()

	t.Run("local to local ignores a default at the target path", func(t *testing.T) {
		t.Parallel()
		f, locks := newCrossProjectOverlayFixture(t)
		f.put(DefaultProject, "/a.md", "default a")
		source := f.put("src", "/own.md", "src own")
		locks.take()
		moved, err := f.move("src", "/own.md", "dst", "/a.md", false, FilePreconditions{ExpectedVersion: source.Version, DestinationMustNotExist: true})
		require.NoError(t, err, "a default is not a physical collision of the target")
		require.Equal(t, 1, moved.MovedCount)
		require.Equal(t, []string{f.auth.APIKeyHash}, locks.take(), "one admission covers the complete move")
		require.Equal(t, "src own", f.read("dst", "/a.md"), "the moved file overrides the default in the target")
		require.Equal(t, "default a", f.read("other", "/a.md"))
		require.True(t, f.physical(DefaultProject, "/a.md"), "creating an override never deletes the default")
		require.False(t, f.physical("src", "/own.md"))
		require.Equal(t, strings.Split(source.Version, ":")[0], strings.Split(f.version("dst", "/a.md"), ":")[0], "the row keeps its incarnation")
	})

	t.Run("inherited source moves out of the default layer", func(t *testing.T) {
		t.Parallel()
		f, locks := newCrossProjectOverlayFixture(t)
		f.put(DefaultProject, "/shared.md", "default v1")
		inherited := f.put(DefaultProject, "/shared.md", "default v2")
		_, err := f.move("src", "/shared.md", "dst", "/shared.md", false, FilePreconditions{ExpectedVersion: "0123456789abcdef0123456789abcdef:1", DestinationMustNotExist: true})
		f.requireCode(err, ErrCodeVersionConflict)
		locks.take()

		moved, err := f.move("src", "/shared.md", "dst", "/shared.md", false, FilePreconditions{ExpectedVersion: f.version("src", "/shared.md"), DestinationMustNotExist: true})
		require.NoError(t, err)
		require.Equal(t, 1, moved.MovedCount)
		require.Equal(t, []string{f.auth.APIKeyHash}, locks.take(), "one admission covers the complete move")
		require.Equal(t, "default v2", f.read("dst", "/shared.md"))
		require.False(t, f.physical(DefaultProject, "/shared.md"), "a move, not a copy")
		_, err = f.svc.Read(f.ctx, f.auth, "other", "/shared.md", 0, -1)
		f.requireCode(err, ErrCodeNotFound)
		require.Equal(t, strings.Split(inherited.Version, ":")[0], strings.Split(f.version("dst", "/shared.md"), ":")[0])
		history, err := f.svc.ListVersions(f.ctx, f.auth, "dst", "/shared.md")
		require.NoError(t, err)
		require.NotEmpty(t, history, "the default's history moves with it")
		require.Equal(t, []string{"UPSERT " + DefaultProject, "UPSERT " + DefaultProject, "DELETE " + DefaultProject, "UPSERT dst"}, f.indexJobs("/shared.md"),
			"the outbox removes the default's index and indexes the target, never src")
	})

	t.Run("local to the default layer and inherited to the same default path", func(t *testing.T) {
		t.Parallel()
		f, _ := newCrossProjectOverlayFixture(t)
		promote := f.put("src", "/promote.md", "promoted")
		moved, err := f.move("src", "/promote.md", DefaultProject, "/promote.md", false, FilePreconditions{ExpectedVersion: promote.Version, DestinationMustNotExist: true})
		require.NoError(t, err)
		require.Equal(t, 1, moved.MovedCount)
		require.Equal(t, "promoted", f.read("other", "/promote.md"), "a project file promoted into the shared layer")
		require.False(t, f.physical("src", "/promote.md"))

		// The inherited file is already the physical destination: nothing moves.
		moved, err = f.move("src", "/promote.md", DefaultProject, "/promote.md", true, FilePreconditions{
			ExpectedVersion: f.version("src", "/promote.md"), ExpectedDestinationVersion: f.version(DefaultProject, "/promote.md"),
		})
		require.NoError(t, err)
		require.Zero(t, moved.MovedCount)
	})

	t.Run("destination conditions see only the physical target", func(t *testing.T) {
		t.Parallel()
		f, _ := newCrossProjectOverlayFixture(t)
		visibleDefault := f.put(DefaultProject, "/taken.md", "default taken")
		source := f.put("src", "/s.md", "source")
		_, err := f.move("src", "/s.md", "dst", "/taken.md", true, FilePreconditions{ExpectedVersion: source.Version, ExpectedDestinationVersion: visibleDefault.Version})
		f.requireCode(err, ErrCodeVersionConflict) // dst has no physical file there

		own := f.put("dst", "/own.md", "dst own")
		_, err = f.move("src", "/s.md", "dst", "/own.md", false, FilePreconditions{ExpectedVersion: source.Version, DestinationMustNotExist: true})
		f.requireCode(err, ErrCodeVersionConflict)
		_, err = f.move("src", "/s.md", "dst", "/own.md", true, FilePreconditions{ExpectedVersion: source.Version, ExpectedDestinationVersion: visibleDefault.Version})
		f.requireCode(err, ErrCodeVersionConflict)
		moved, err := f.move("src", "/s.md", "dst", "/own.md", true, FilePreconditions{ExpectedVersion: source.Version, ExpectedDestinationVersion: own.Version})
		require.NoError(t, err)
		require.Equal(t, 1, moved.MovedCount)
		require.Equal(t, "source", f.read("dst", "/own.md"))
		snapshots, err := f.svc.ListVersions(f.ctx, f.auth, "dst", "/own.md")
		require.NoError(t, err)
		var replaced bool
		for _, version := range snapshots {
			snapshot, readErr := f.svc.ReadVersion(f.ctx, f.auth, "dst", "/own.md", version.ID)
			require.NoError(t, readErr)
			replaced = replaced || string(snapshot.Content) == "dst own"
		}
		require.True(t, replaced, "the replaced destination keeps a history snapshot")
	})
}

// TestDefaultSameProjectRenameReplacesOnlyVisibleFiles pins what an overwriting
// same-project rename replaces when the default layer is involved.
func TestDefaultSameProjectRenameReplacesOnlyVisibleFiles(t *testing.T) {
	t.Parallel()
	f := newOverlayFixture(t)
	f.put(DefaultProject, "/d.md", "default source")
	f.put(DefaultProject, "/hidden.md", "default hidden by p")
	f.svc.mustWritePhysical(t, f.auth, "p", "/hidden.md", "p override")

	// The visible destination is p's override, but a default p hides also sits
	// at the name. Its version was never visible, so it is not replaced.
	_, err := f.svc.Rename(f.ctx, f.auth, RenameRequest{Project: "p", FromPath: "/d.md", ToPath: "/hidden.md", Overwrite: true})
	f.requireCode(err, ErrCodeAlreadyExists)
	require.Equal(t, "default hidden by p", f.read("q", "/hidden.md"))
	require.Equal(t, "p override", f.read("p", "/hidden.md"))
	require.Equal(t, "default source", f.read("q", "/d.md"))

	// The conditioned form judges the same visible destination.
	ctx, err := WithFilePreconditions(f.ctx, f.auth, "p", "/d.md", FileOperationRename, FilePreconditions{
		ExpectedVersion: f.version("p", "/d.md"), DestinationPath: "/hidden.md", ExpectedDestinationVersion: f.version("p", "/hidden.md"),
	})
	require.NoError(t, err)
	_, err = f.svc.Rename(ctx, f.auth, RenameRequest{Project: "p", FromPath: "/d.md", ToPath: "/hidden.md", Overwrite: true})
	f.requireCode(err, ErrCodeAlreadyExists)
}
