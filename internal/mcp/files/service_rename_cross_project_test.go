package files

import (
	"context"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// newCrossProjectTestService builds a SQLite service with the default advisory
// lock provider, which supports multi-project mutations.
func newCrossProjectTestService(t *testing.T, maxProjectBytes int64) *Service {
	t.Helper()
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = maxProjectBytes
	return newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})})
}

// crossProjectTestAuth returns the tenant used by cross-project rename tests.
func crossProjectTestAuth() AuthContext {
	return AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
}

// mustCrossProjectWrite writes content and returns the committed version.
func mustCrossProjectWrite(t *testing.T, svc *Service, auth AuthContext, project, path, content string) string {
	t.Helper()
	result, err := svc.Write(context.Background(), auth, project, path, content, "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NotEmpty(t, result.Version)
	return result.Version
}

// requireCrossProjectContent asserts the live content of one path.
func requireCrossProjectContent(t *testing.T, svc *Service, auth AuthContext, project, path, want string) {
	t.Helper()
	read, err := svc.Read(context.Background(), auth, project, path, 0, -1)
	require.NoError(t, err, "%s:%s", project, path)
	require.Equal(t, want, read.Content, "%s:%s", project, path)
}

// requireCrossProjectMissing asserts one path is absent.
func requireCrossProjectMissing(t *testing.T, svc *Service, auth AuthContext, project, path string) {
	t.Helper()
	_, err := svc.Read(context.Background(), auth, project, path, 0, -1)
	require.Error(t, err, "%s:%s", project, path)
	require.True(t, IsCode(err, ErrCodeNotFound), "%s:%s: %v", project, path, err)
}

// countCrossProjectRows counts rows of a tracked table for one project and path column value.
func countCrossProjectRows(t *testing.T, svc *Service, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, svc.db.QueryRowContext(context.Background(), rebindSQL(query, svc.isPostgres), args...).Scan(&count))
	return count
}

// TestCrossProjectRenameMovesFileIdentityAndHistory verifies that a
// cross-project move carries the row, its incarnation and its version history
// into the target project, advances the revision, and re-indexes it there.
func TestCrossProjectRenameMovesFileIdentityAndHistory(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/docs/a.txt", "first")
	before := mustCrossProjectWrite(t, svc, auth, "alpha", "/docs/a.txt", "second")
	history := countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_file_versions WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ''`,
		auth.APIKeyHash, "alpha", "/docs/a.txt")
	require.Positive(t, history, "the overwrite must have produced a retained version")

	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/docs/a.txt", TargetProject: "beta", ToPath: "/inbox/a.txt"})
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)

	requireCrossProjectMissing(t, svc, auth, "alpha", "/docs/a.txt")
	requireCrossProjectContent(t, svc, auth, "beta", "/inbox/a.txt", "second")

	stat, err := svc.Stat(ctx, auth, "beta", "/inbox/a.txt")
	require.NoError(t, err)
	beforeIdentity, beforeRevision, _ := strings.Cut(before, ":")
	afterIdentity, afterRevision, _ := strings.Cut(stat.Version, ":")
	require.Equal(t, beforeIdentity, afterIdentity, "a move preserves the file incarnation")
	require.NotEqual(t, beforeRevision, afterRevision, "a move advances the revision so old tokens go stale")

	require.Zero(t, countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_file_versions WHERE apikey_hash = ? AND project = ? AND system_owner = ''`,
		auth.APIKeyHash, "alpha"), "no history may stay behind in the source project")
	require.Equal(t, history, countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_file_versions WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ''`,
		auth.APIKeyHash, "beta", "/inbox/a.txt"), "history travels with the file")

	require.Equal(t, 1, countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND system_owner = ''`,
		auth.APIKeyHash, "alpha", "/docs/a.txt", indexOperationDelete), "the source index entry must be removed")
	require.Equal(t, 1, countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND system_owner = ''`,
		auth.APIKeyHash, "beta", "/inbox/a.txt", indexOperationUpsert), "the target index entry must be rebuilt")

	listed, err := svc.List(ctx, auth, "alpha", "", 5, 100)
	require.NoError(t, err)
	require.Empty(t, listed.Entries, "the source project must not list the moved file")
}

// TestCrossProjectRenameDirectorySubtree verifies directory moves and that the
// own-subtree rule only applies within one project.
func TestCrossProjectRenameDirectorySubtree(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/docs/a.txt", "a")
	mustCrossProjectWrite(t, svc, auth, "alpha", "/docs/sub/b.txt", "b")
	mustCrossProjectWrite(t, svc, auth, "alpha", "/keep.txt", "keep")

	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/docs", TargetProject: "beta", ToPath: "/docs/archive"})
	require.NoError(t, err, "a destination under the same name in another project is not the source subtree")
	require.Equal(t, 2, result.MovedCount)

	requireCrossProjectMissing(t, svc, auth, "alpha", "/docs/a.txt")
	requireCrossProjectMissing(t, svc, auth, "alpha", "/docs/sub/b.txt")
	requireCrossProjectContent(t, svc, auth, "alpha", "/keep.txt", "keep")
	requireCrossProjectContent(t, svc, auth, "beta", "/docs/archive/a.txt", "a")
	requireCrossProjectContent(t, svc, auth, "beta", "/docs/archive/sub/b.txt", "b")
}

// TestCrossProjectRenameSamePathIsAMove verifies that keeping the path while
// changing the project moves the file instead of being a same-path no-op.
func TestCrossProjectRenameSamePathIsAMove(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/a.txt", "a")
	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/a.txt"})
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectMissing(t, svc, auth, "alpha", "/a.txt")
	requireCrossProjectContent(t, svc, auth, "beta", "/a.txt", "a")

	// An empty or identical target keeps the existing same-project semantics.
	result, err = svc.Rename(ctx, auth, RenameRequest{Project: "beta", FromPath: "/a.txt", TargetProject: "", ToPath: "/a.txt"})
	require.NoError(t, err)
	require.Zero(t, result.MovedCount)
	result, err = svc.Rename(ctx, auth, RenameRequest{Project: "beta", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt"})
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectContent(t, svc, auth, "beta", "/b.txt", "a")
}

// TestCrossProjectRenameDestinationRules verifies that collision, overwrite
// and parent rules are evaluated in the target project, and that a failed move
// leaves both projects unchanged.
func TestCrossProjectRenameDestinationRules(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/a.txt", "source")
	mustCrossProjectWrite(t, svc, auth, "beta", "/a.txt", "target")
	mustCrossProjectWrite(t, svc, auth, "beta", "/file", "plain file")
	mustCrossProjectWrite(t, svc, auth, "beta", "/dir/child.txt", "child")

	_, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/a.txt"})
	require.True(t, IsCode(err, ErrCodeAlreadyExists), "%v", err)
	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/file/a.txt"})
	require.True(t, IsCode(err, ErrCodeNotDirectory), "%v", err)
	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/dir", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeAlreadyExists), "a file cannot replace a target directory: %v", err)
	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/missing.txt", TargetProject: "beta", ToPath: "/new.txt"})
	require.True(t, IsCode(err, ErrCodeNotFound), "%v", err)
	requireCrossProjectContent(t, svc, auth, "alpha", "/a.txt", "source")
	requireCrossProjectContent(t, svc, auth, "beta", "/a.txt", "target")

	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/a.txt", Overwrite: true})
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectMissing(t, svc, auth, "alpha", "/a.txt")
	requireCrossProjectContent(t, svc, auth, "beta", "/a.txt", "source")
	require.Equal(t, 1, countCrossProjectRows(t, svc,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ''`,
		auth.APIKeyHash, "beta", "/a.txt"), "the overwritten target row must be released")
}

// TestCrossProjectRenameIsTenantScoped verifies that the target project is
// resolved inside the caller's own namespace only.
func TestCrossProjectRenameIsTenantScoped(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	other := AuthContext{APIKeyHash: "other-hash", APIKey: "other-key", UserIdentity: "user:other"}
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/a.txt", "mine")
	mustCrossProjectWrite(t, svc, other, "beta", "/a.txt", "theirs")

	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/a.txt"})
	require.NoError(t, err, "another tenant's file at the same project and path is not a collision")
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectContent(t, svc, auth, "beta", "/a.txt", "mine")
	requireCrossProjectContent(t, svc, other, "beta", "/a.txt", "theirs")
	requireCrossProjectMissing(t, svc, other, "alpha", "/a.txt")
}

// TestCrossProjectRenameEnforcesTargetQuota verifies that moved bytes count
// against the target project, after any overwritten destination is released.
func TestCrossProjectRenameEnforcesTargetQuota(t *testing.T) {
	svc := newCrossProjectTestService(t, 20)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	mustCrossProjectWrite(t, svc, auth, "alpha", "/big.txt", "0123456789")
	mustCrossProjectWrite(t, svc, auth, "beta", "/full.txt", "0123456789AB")

	_, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/big.txt", TargetProject: "beta", ToPath: "/big.txt"})
	require.True(t, IsCode(err, ErrCodeQuotaExceeded), "%v", err)
	requireCrossProjectContent(t, svc, auth, "alpha", "/big.txt", "0123456789")
	requireCrossProjectMissing(t, svc, auth, "beta", "/big.txt")

	result, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/big.txt", TargetProject: "beta", ToPath: "/full.txt", Overwrite: true})
	require.NoError(t, err, "replacing the large destination frees enough room")
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectContent(t, svc, auth, "beta", "/full.txt", "0123456789")
}

// TestCrossProjectRenameValidatesTargetProject verifies that an invalid target
// is refused, named as the target, and never widened to every project.
func TestCrossProjectRenameValidatesTargetProject(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()
	mustCrossProjectWrite(t, svc, auth, "alpha", "/a.txt", "a")

	for _, target := range []string{ProjectWildcard, "bad/project", " ", strings.Repeat("p", 129)} {
		_, err := svc.Rename(ctx, auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: target, ToPath: "/a.txt"})
		require.True(t, IsCode(err, ErrCodeInvalidPath), "%q: %v", target, err)
		require.ErrorContains(t, err, "target project", "%q", target)
	}
	requireCrossProjectContent(t, svc, auth, "alpha", "/a.txt", "a")
}

// TestCrossProjectRenamePreconditions verifies that source conditions are
// checked in the source project and destination conditions in the target one.
func TestCrossProjectRenamePreconditions(t *testing.T) {
	svc := newCrossProjectTestService(t, 10_000)
	auth := crossProjectTestAuth()
	ctx := context.Background()

	source := mustCrossProjectWrite(t, svc, auth, "alpha", "/a.txt", "source")
	sameNameInSource := mustCrossProjectWrite(t, svc, auth, "alpha", "/b.txt", "decoy")
	target := mustCrossProjectWrite(t, svc, auth, "beta", "/b.txt", "target")

	conditioned := func(p FilePreconditions, destination, destinationProject string) context.Context {
		t.Helper()
		p.DestinationPath = destination
		p.DestinationProject = destinationProject
		conditionalCtx, err := WithFilePreconditions(ctx, auth, "alpha", "/a.txt", FileOperationRename, p)
		require.NoError(t, err)
		return conditionalCtx
	}

	_, err := svc.Rename(conditioned(FilePreconditions{ExpectedVersion: source, DestinationMustNotExist: true}, "/b.txt", "beta"), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeVersionConflict), "the target project's destination exists: %v", err)

	_, err = svc.Rename(conditioned(FilePreconditions{ExpectedVersion: source, ExpectedDestinationVersion: sameNameInSource}, "/b.txt", "beta"), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeVersionConflict), "a token of the same path in the source project must not match: %v", err)

	_, err = svc.Rename(conditioned(FilePreconditions{ExpectedVersion: source, ExpectedDestinationVersion: target}, "/b.txt", "gamma"), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeInvalidArgument), "conditions scoped to another destination project: %v", err)

	_, err = svc.Rename(conditioned(FilePreconditions{ExpectedVersion: source, ExpectedDestinationVersion: target}, "/b.txt", ""), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeInvalidArgument), "conditions without a destination project describe the source project: %v", err)

	_, err = svc.Rename(conditioned(FilePreconditions{ExpectedVersion: target, ExpectedDestinationVersion: target}, "/b.txt", "beta"), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.True(t, IsCode(err, ErrCodeVersionConflict), "the source token is checked in the source project: %v", err)
	requireCrossProjectContent(t, svc, auth, "alpha", "/a.txt", "source")
	requireCrossProjectContent(t, svc, auth, "beta", "/b.txt", "target")

	result, err := svc.Rename(conditioned(FilePreconditions{ExpectedVersion: source, ExpectedDestinationVersion: target}, "/b.txt", "beta"), auth, RenameRequest{Project: "alpha", FromPath: "/a.txt", TargetProject: "beta", ToPath: "/b.txt", Overwrite: true})
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)
	requireCrossProjectContent(t, svc, auth, "beta", "/b.txt", "source")
	requireCrossProjectContent(t, svc, auth, "alpha", "/b.txt", "decoy")

	_, err = WithFilePreconditions(ctx, auth, "alpha", "/a.txt", FileOperationWrite, FilePreconditions{ExpectedVersion: source, DestinationProject: "beta"})
	require.True(t, IsCode(err, ErrCodeInvalidArgument), "a destination project is rename-only: %v", err)
}
