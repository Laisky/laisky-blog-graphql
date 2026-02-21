package files

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// TestRenameFileSuccess verifies single-file rename behavior.
func TestRenameFileSuccess(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "hello", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	result, err := svc.Rename(context.Background(), auth, "proj", "/docs/a.txt", "/docs/b.txt", false)
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)

	_, err = svc.Read(context.Background(), auth, "proj", "/docs/a.txt", 0, -1)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))

	readResult, err := svc.Read(context.Background(), auth, "proj", "/docs/b.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "hello", readResult.Content)
}

// TestRenameDirectorySuccess verifies directory subtree move behavior.
func TestRenameDirectorySuccess(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "a", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/docs/sub/b.txt", "b", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	result, err := svc.Rename(context.Background(), auth, "proj", "/docs", "/archive", false)
	require.NoError(t, err)
	require.Equal(t, 2, result.MovedCount)

	_, err = svc.Read(context.Background(), auth, "proj", "/docs/a.txt", 0, -1)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeNotFound))

	readA, err := svc.Read(context.Background(), auth, "proj", "/archive/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "a", readA.Content)

	readB, err := svc.Read(context.Background(), auth, "proj", "/archive/sub/b.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "b", readB.Content)
}

// TestRenameDestinationExistsWithoutOverwrite verifies destination collisions are rejected.
func TestRenameDestinationExistsWithoutOverwrite(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "source", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/b.txt", "target", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Rename(context.Background(), auth, "proj", "/a.txt", "/b.txt", false)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeAlreadyExists))
}

// TestRenameFileOverwrite verifies overwrite replacement for file rename.
func TestRenameFileOverwrite(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "source", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/b.txt", "target", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	result, err := svc.Rename(context.Background(), auth, "proj", "/a.txt", "/b.txt", true)
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)

	readResult, err := svc.Read(context.Background(), auth, "proj", "/b.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "source", readResult.Content)
}

// TestRenameDirectoryToOwnSubtreeRejected verifies directory self-subtree moves are invalid.
func TestRenameDirectoryToOwnSubtreeRejected(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "a", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	_, err = svc.Rename(context.Background(), auth, "proj", "/docs", "/docs/sub/new", false)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeInvalidPath))
}

// TestRenameNoop verifies from_path equal to to_path returns a successful no-op.
func TestRenameNoop(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "a", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	result, err := svc.Rename(context.Background(), auth, "proj", "/docs/a.txt", "/docs/a.txt", false)
	require.NoError(t, err)
	require.Equal(t, 0, result.MovedCount)
}
