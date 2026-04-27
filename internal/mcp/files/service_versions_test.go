package files

import (
	"context"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// versionsTestSettings returns a Settings instance suitable for version tests.
func versionsTestSettings() Settings {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000
	return settings
}

// newVersionsTestService constructs a service for version tests.
func newVersionsTestService(t *testing.T) *Service {
	t.Helper()
	return newTestService(t, versionsTestSettings(), testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
}

// versionsTestAuth returns the canonical auth context for version tests.
func versionsTestAuth() AuthContext {
	return AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
}

// seedVersionsAtTimes inserts version rows for path with explicit created_at timestamps.
func seedVersionsAtTimes(t *testing.T, svc *Service, auth AuthContext, project, path string, sourceFileID uint64, times []time.Time) []uint64 {
	t.Helper()
	ids := make([]uint64, 0, len(times))
	for i, ts := range times {
		res, err := svc.db.ExecContext(context.Background(),
			rebindSQL(`INSERT INTO mcp_file_versions (apikey_hash, project, path, content, size, created_at, source_file_id)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, svc.isPostgres),
			auth.APIKeyHash,
			project,
			path,
			[]byte("seed"),
			int64(len("seed")),
			ts,
			sourceFileID,
		)
		require.NoError(t, err, "seed version %d", i)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		ids = append(ids, uint64(id))
	}
	return ids
}

// TestVersions_FirstWriteCreatesNoVersion verifies a brand-new file has no version history.
func TestVersions_FirstWriteCreatesNoVersion(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 0)
}

// TestVersions_SecondWriteSnapshotsPriorContent verifies the second write snapshots the prior bytes.
func TestVersions_SecondWriteSnapshotsPriorContent(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)

	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)
	require.Equal(t, int64(1), read.Size)
}

// TestVersions_AppendModeAlsoSnapshots verifies append mode writes still snapshot prior bytes.
func TestVersions_AppendModeAlsoSnapshots(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", " B", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)

	current, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "A B", current.Content)
}

// TestVersions_OverwriteModeSnapshots verifies overwrite mode snapshots prior bytes before patching.
func TestVersions_OverwriteModeSnapshots(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "ABCDE", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "XX", "utf-8", 2, WriteModeOverwrite)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("ABCDE"), read.Content)

	current, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "ABXXE", current.Content)
}

// TestVersions_RetentionTop3WhenAllOld verifies that when every snapshot is older than the
// retention window the prune step keeps exactly the top N newest rows.
func TestVersions_RetentionTop3WhenAllOld(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "current", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var fileID uint64
	require.NoError(t, svc.db.QueryRowContext(context.Background(),
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE`, svc.isPostgres),
		auth.APIKeyHash, "proj", "/a.txt").Scan(&fileID))

	now := svc.clock()
	old := now.Add(-versionRetentionWindow - time.Hour)
	timestamps := []time.Time{
		old.Add(-5 * time.Hour),
		old.Add(-4 * time.Hour),
		old.Add(-3 * time.Hour),
		old.Add(-2 * time.Hour),
		old.Add(-1 * time.Hour),
	}
	seedVersionsAtTimes(t, svc, auth, "proj", "/a.txt", fileID, timestamps)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "next", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, versionRetentionTopN)
}

// TestVersions_RetentionWithinWindow verifies all rows within the retention window survive prune.
func TestVersions_RetentionWithinWindow(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "v0", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var fileID uint64
	require.NoError(t, svc.db.QueryRowContext(context.Background(),
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE`, svc.isPostgres),
		auth.APIKeyHash, "proj", "/a.txt").Scan(&fileID))

	now := svc.clock()
	timestamps := []time.Time{
		now.Add(-5 * time.Hour),
		now.Add(-4 * time.Hour),
		now.Add(-3 * time.Hour),
		now.Add(-2 * time.Hour),
		now.Add(-1 * time.Hour),
	}
	seedVersionsAtTimes(t, svc, auth, "proj", "/a.txt", fileID, timestamps)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "v1", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 6)
}

// TestVersions_RetentionMixed verifies the union retention rule (top-N OR within window).
func TestVersions_RetentionMixed(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "current", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var fileID uint64
	require.NoError(t, svc.db.QueryRowContext(context.Background(),
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE`, svc.isPostgres),
		auth.APIKeyHash, "proj", "/a.txt").Scan(&fileID))

	now := svc.clock()
	old := now.Add(-versionRetentionWindow - time.Hour)
	timestamps := []time.Time{
		now.Add(-1 * time.Hour),
		old.Add(-1 * time.Hour),
		old.Add(-2 * time.Hour),
		old.Add(-3 * time.Hour),
		old.Add(-4 * time.Hour),
	}
	seedVersionsAtTimes(t, svc, auth, "proj", "/a.txt", fileID, timestamps)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "next", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, versionRetentionTopN)
	require.True(t, versions[0].CreatedAt.After(versions[1].CreatedAt) || versions[0].CreatedAt.Equal(versions[1].CreatedAt))
}

// TestVersions_DeleteSnapshotsLastContent verifies Delete snapshots the last content.
func TestVersions_DeleteSnapshotsLastContent(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/file.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.Delete(context.Background(), auth, "proj", "/file.txt", false)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/file.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/file.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)
}

// TestVersions_DeleteRecursiveSnapshotsAll verifies recursive delete snapshots every member.
func TestVersions_DeleteRecursiveSnapshotsAll(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/a.txt", "AA", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/dir/b.txt", "BB", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.Delete(context.Background(), auth, "proj", "/dir", true)
	require.NoError(t, err)

	versionsA, err := svc.ListVersions(context.Background(), auth, "proj", "/dir/a.txt")
	require.NoError(t, err)
	require.Len(t, versionsA, 1)
	versionsB, err := svc.ListVersions(context.Background(), auth, "proj", "/dir/b.txt")
	require.NoError(t, err)
	require.Len(t, versionsB, 1)
}

// TestVersions_RenameMovesHistory verifies rename moves version rows along with files.
func TestVersions_RenameMovesHistory(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/old.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/old.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.Rename(context.Background(), auth, "proj", "/old.txt", "/new.txt", false)
	require.NoError(t, err)

	oldVersions, err := svc.ListVersions(context.Background(), auth, "proj", "/old.txt")
	require.NoError(t, err)
	require.Len(t, oldVersions, 0)

	newVersions, err := svc.ListVersions(context.Background(), auth, "proj", "/new.txt")
	require.NoError(t, err)
	require.Len(t, newVersions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/new.txt", newVersions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)
}

// TestVersions_RenameDirectoryMovesHistory verifies directory renames remap version paths.
func TestVersions_RenameDirectoryMovesHistory(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/a.txt", "A1", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/dir/a.txt", "A2", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.Rename(context.Background(), auth, "proj", "/dir", "/dir2", false)
	require.NoError(t, err)

	oldVersions, err := svc.ListVersions(context.Background(), auth, "proj", "/dir/a.txt")
	require.NoError(t, err)
	require.Len(t, oldVersions, 0)

	newVersions, err := svc.ListVersions(context.Background(), auth, "proj", "/dir2/a.txt")
	require.NoError(t, err)
	require.Len(t, newVersions, 1)
}

// TestVersions_RestoreCreatesNewSnapshot verifies a successful restore snapshots the prior current.
func TestVersions_RestoreCreatesNewSnapshot(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	originalA := versions[0].ID

	_, err = svc.RestoreVersion(context.Background(), auth, "proj", "/a.txt", originalA)
	require.NoError(t, err)

	current, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "A", current.Content)

	versions2, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions2, 2)

	hasB := false
	for _, v := range versions2 {
		read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", v.ID)
		require.NoError(t, err)
		if string(read.Content) == "B" {
			hasB = true
		}
	}
	require.True(t, hasB, "restore must snapshot the prior current B")
}

// TestVersions_RestoreNonexistentReturns404 verifies an unknown id surfaces NotFound.
func TestVersions_RestoreNonexistentReturns404(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.RestoreVersion(context.Background(), auth, "proj", "/a.txt", 999999)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))
}

// TestVersions_RestoreCrossTenantReturns404 verifies cross-tenant restore is denied as not found.
func TestVersions_RestoreCrossTenantReturns404(t *testing.T) {
	svc := newVersionsTestService(t)
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	_, err := svc.Write(context.Background(), authA, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authA, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), authA, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)

	_, err = svc.RestoreVersion(context.Background(), authB, "proj", "/a.txt", versions[0].ID)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))

	_, err = svc.ReadVersion(context.Background(), authB, "proj", "/a.txt", versions[0].ID)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))
}

// TestVersions_RestoreCrossProjectReturns404 verifies cross-project restore is denied.
func TestVersions_RestoreCrossProjectReturns404(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "p1", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "p1", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "p1", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)

	_, err = svc.RestoreVersion(context.Background(), auth, "p2", "/a.txt", versions[0].ID)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))
}

// TestVersions_RestoreOnPathThatNoLongerHasVersion_AfterRename verifies restore by old path
// returns NotFound after a rename, while the new path still resolves the version.
func TestVersions_RestoreOnPathThatNoLongerHasVersion_AfterRename(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/old.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/old.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versionsBefore, err := svc.ListVersions(context.Background(), auth, "proj", "/old.txt")
	require.NoError(t, err)
	require.Len(t, versionsBefore, 1)
	versionID := versionsBefore[0].ID

	_, err = svc.Rename(context.Background(), auth, "proj", "/old.txt", "/new.txt", false)
	require.NoError(t, err)

	_, err = svc.RestoreVersion(context.Background(), auth, "proj", "/old.txt", versionID)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))

	_, err = svc.RestoreVersion(context.Background(), auth, "proj", "/new.txt", versionID)
	require.NoError(t, err)
	current, err := svc.Read(context.Background(), auth, "proj", "/new.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "A", current.Content)
}

// TestVersions_AuthBoundary_ListVersions verifies tenants only see their own version rows.
func TestVersions_AuthBoundary_ListVersions(t *testing.T) {
	svc := newVersionsTestService(t)
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	_, err := svc.Write(context.Background(), authA, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authA, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	_, err = svc.Write(context.Background(), authB, "proj", "/a.txt", "X", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authB, "proj", "/a.txt", "Y", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versA, err := svc.ListVersions(context.Background(), authA, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versA, 1)
	readA, err := svc.ReadVersion(context.Background(), authA, "proj", "/a.txt", versA[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), readA.Content)

	versB, err := svc.ListVersions(context.Background(), authB, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versB, 1)
	readB, err := svc.ReadVersion(context.Background(), authB, "proj", "/a.txt", versB[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("X"), readB.Content)
}

// TestVersions_RestoreSameContent_StillSnapshots verifies that writing identical content still snapshots.
func TestVersions_RestoreSameContent_StillSnapshots(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)
}

// TestVersions_EmptyContentSnapshot verifies truncating to empty still preserves prior content.
func TestVersions_EmptyContentSnapshot(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	read, err := svc.ReadVersion(context.Background(), auth, "proj", "/a.txt", versions[0].ID)
	require.NoError(t, err)
	require.Equal(t, []byte("A"), read.Content)

	current, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "", current.Content)
}

// TestVersions_PruneAfterDelete verifies retention is enforced when a soft-delete snapshots.
func TestVersions_PruneAfterDelete(t *testing.T) {
	svc := newVersionsTestService(t)
	auth := versionsTestAuth()

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "current", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var fileID uint64
	require.NoError(t, svc.db.QueryRowContext(context.Background(),
		rebindSQL(`SELECT id FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE`, svc.isPostgres),
		auth.APIKeyHash, "proj", "/a.txt").Scan(&fileID))

	now := svc.clock()
	old := now.Add(-versionRetentionWindow - 2*time.Hour)
	timestamps := []time.Time{
		old.Add(-5 * time.Hour),
		old.Add(-4 * time.Hour),
		old.Add(-3 * time.Hour),
		old.Add(-2 * time.Hour),
	}
	seedVersionsAtTimes(t, svc, auth, "proj", "/a.txt", fileID, timestamps)

	_, err = svc.Delete(context.Background(), auth, "proj", "/a.txt", false)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, versionRetentionTopN)
}
