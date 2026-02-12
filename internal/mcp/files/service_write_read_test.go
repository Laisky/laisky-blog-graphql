package files

import (
	"context"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// TestWriteReadFlow verifies write/read/overwrite behavior.
func TestWriteReadFlow(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	writeRes, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.Equal(t, int64(5), writeRes.BytesWritten)

	readRes, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "hello", readRes.Content)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "yo", "utf-8", 0, WriteModeOverwrite)
	require.NoError(t, err)
	readRes, err = svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "yollo", readRes.Content)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "new", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	readRes, err = svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "new", readRes.Content)
}

// TestDeleteAndListFlow verifies delete and list behavior.
func TestDeleteAndListFlow(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/file.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	listRes, err := svc.List(context.Background(), auth, "proj", "/dir", 1, 10)
	require.NoError(t, err)
	require.Len(t, listRes.Entries, 1)
	require.Equal(t, "/dir/file.txt", listRes.Entries[0].Path)

	_, err = svc.Delete(context.Background(), auth, "proj", "/dir", false)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotEmpty))

	deleteRes, err := svc.Delete(context.Background(), auth, "proj", "/dir", true)
	require.NoError(t, err)
	require.Equal(t, 1, deleteRes.DeletedCount)

	statRes, err := svc.Stat(context.Background(), auth, "proj", "/dir/file.txt")
	require.NoError(t, err)
	require.False(t, statRes.Exists)
}

// TestRootDeleteForbidden verifies root delete is forbidden regardless configuration.
func TestRootDeleteForbidden(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.AllowRootWipe = false
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Delete(context.Background(), auth, "proj", "", true)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePermissionDenied))

	settings.AllowRootWipe = true
	svc = newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Delete(context.Background(), auth, "proj", "", true)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePermissionDenied))
}

// TestFileStatDirectoryUpdatedAt verifies directory stat updated_at behavior.
func TestFileStatDirectoryUpdatedAt(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	clock := func() time.Time { return time.Date(2026, 2, 11, 1, 2, 3, 0, time.UTC) }
	credential, err := NewCredentialProtector(settings.Security)
	require.NoError(t, err)
	svc, err := NewService(newTestDB(t), settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, nil, credential, &memoryCredentialStore{}, nil, nil, clock)
	require.NoError(t, err)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err = svc.Write(context.Background(), auth, "proj", "/dir/file.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	statRes, err := svc.Stat(context.Background(), auth, "proj", "/dir")
	require.NoError(t, err)
	require.True(t, statRes.Exists)
	require.Equal(t, FileTypeDirectory, statRes.Type)
	require.Equal(t, clock(), statRes.UpdatedAt)
}

// TestWriteInvalidOffset verifies overwrite offset bounds.
func TestWriteInvalidOffset(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "world", "utf-8", 99, WriteModeOverwrite)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeInvalidOffset))
}

// TestProjectQuotaEnforced verifies project quota enforcement.
func TestProjectQuotaEnforced(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 4

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeQuotaExceeded))
}

// TestReadOffsetBeyondEOF returns empty content.
func TestReadOffsetBeyondEOF(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	readRes, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 99, -1)
	require.NoError(t, err)
	require.Equal(t, "", readRes.Content)
}

// TestWritePathConflictWithDirectory verifies writes fail when the target has descendants.
func TestWritePathConflictWithDirectory(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/file.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Write(context.Background(), auth, "proj", "/dir", "data", "utf-8", 0, WriteModeAppend)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeIsDirectory))
}

// TestWritePathConflictWithParentFile verifies nested writes fail when a parent is a file.
func TestWritePathConflictWithParentFile(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Write(context.Background(), auth, "proj", "/dir/file.txt", "data", "utf-8", 0, WriteModeAppend)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotDirectory))
}

// TestReadDirectoryReturnsError verifies reading a directory path returns IS_DIRECTORY.
func TestReadDirectoryReturnsError(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/file.txt", "data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Read(context.Background(), auth, "proj", "/dir", 0, -1)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeIsDirectory))
}

// TestListSortedAndLimited verifies list ordering and limit truncation behavior.
func TestListSortedAndLimited(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/b.txt", "b", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "a", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/c.txt", "c", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	listRes, err := svc.List(context.Background(), auth, "proj", "", 1, 2)
	require.NoError(t, err)
	require.True(t, listRes.HasMore)
	require.Len(t, listRes.Entries, 2)
	require.Equal(t, "/a.txt", listRes.Entries[0].Path)
	require.Equal(t, "/b.txt", listRes.Entries[1].Path)
}

// TestTenantIsolation verifies data is isolated by API key hash.
func TestTenantIsolation(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	_, err := svc.Write(context.Background(), authA, "proj", "/a.txt", "alpha", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Read(context.Background(), authB, "proj", "/a.txt", 0, -1)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))

	listB, err := svc.List(context.Background(), authB, "proj", "", 1, 10)
	require.NoError(t, err)
	require.Len(t, listB.Entries, 0)

	_, err = svc.Write(context.Background(), authB, "proj", "/a.txt", "bravo", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	readA, err := svc.Read(context.Background(), authA, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "alpha", readA.Content)
	readB, err := svc.Read(context.Background(), authB, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "bravo", readB.Content)
}

// TestDeleteRootAlwaysForbidden verifies root delete is always denied.
func TestDeleteRootAlwaysForbidden(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Delete(context.Background(), auth, "proj", "", false)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePermissionDenied))

	_, err = svc.Delete(context.Background(), auth, "proj", "", true)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePermissionDenied))
}

// TestStatRootExistsWhenEmpty verifies root stat exists even with no files.
func TestStatRootExistsWhenEmpty(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	statRes, err := svc.Stat(context.Background(), auth, "proj", "")
	require.NoError(t, err)
	require.True(t, statRes.Exists)
	require.Equal(t, FileTypeDirectory, statRes.Type)
	require.True(t, statRes.UpdatedAt.IsZero())
}

// TestStatRootDirectory verifies root stat returns directory metadata when files exist.
func TestStatRootDirectory(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	statRes, err := svc.Stat(context.Background(), auth, "proj", "")
	require.NoError(t, err)
	require.True(t, statRes.Exists)
	require.Equal(t, FileTypeDirectory, statRes.Type)
}
