package files

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// newPrefixTestService builds a SQLite-backed service with search disabled.
func newPrefixTestService(t *testing.T) (*Service, AuthContext) {
	t.Helper()
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 1_000_000
	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	return svc, AuthContext{APIKeyHash: "prefix-hash", APIKey: "prefix-key", UserIdentity: "user:prefix"}
}

// TestDirectoryPrefixTreatsUnderscoreLiterally reproduces SQL LIKE wildcard
// leakage: "_" is a legal path character, so the directory "/my_dir" must not
// match descendants of the sibling "/myXdir".
func TestDirectoryPrefixTreatsUnderscoreLiterally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, auth := newPrefixTestService(t)

	_, err := svc.Write(ctx, auth, "proj", "/myXdir/child.txt", "sibling", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	stat, err := svc.Stat(ctx, auth, "proj", "/my_dir")
	require.NoError(t, err)
	require.False(t, stat.Exists, "/my_dir has no descendants; /myXdir must not satisfy its prefix")

	_, err = svc.List(ctx, auth, "proj", "/my_dir", 1, 10)
	require.True(t, IsCode(err, ErrCodeNotFound), "listing /my_dir must not surface /myXdir entries: %v", err)

	// A file named like the wildcard-matching directory is legal.
	_, err = svc.Write(ctx, auth, "proj", "/my_dir", "file", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err, "a file at /my_dir must not collide with /myXdir/child.txt")

	_, err = svc.Delete(ctx, auth, "proj", "/my_dir", false)
	require.NoError(t, err)
	read, err := svc.Read(ctx, auth, "proj", "/myXdir/child.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "sibling", read.Content)
}

// TestSearchPathPrefixTreatsUnderscoreLiterally keeps the raw-file search
// fallback's path_prefix literal as well.
func TestSearchPathPrefixTreatsUnderscoreLiterally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	settings := LoadSettingsFromConfig()
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Search.Enabled = true
	svc := newTestService(t, settings, nil, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "prefix-search", APIKey: "key"}

	_, err := svc.Write(ctx, auth, "proj", "/a_b/hit.txt", "needle literal", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(ctx, auth, "proj", "/aXb/miss.txt", "needle wildcard", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	result, err := svc.Search(ctx, auth, "proj", "needle", "/a_b", 10)
	require.NoError(t, err)
	require.Len(t, result.Chunks, 1)
	require.Equal(t, "/a_b/hit.txt", result.Chunks[0].FilePath)
}
