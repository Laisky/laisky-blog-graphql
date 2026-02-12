package files

import (
	"context"
	"strings"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

type stubRerankClient struct {
	scores []float64
	err    error
}

// Rerank returns configured rerank scores or an error.
func (s stubRerankClient) Rerank(context.Context, string, string, []string) ([]float64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.scores, nil
}

// TestSearchEndToEnd verifies indexing and search retrieval.
func TestSearchEndToEnd(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 8
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/notes.txt", "hello world", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "hello", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Contains(t, searchRes.Chunks[0].ChunkContent, "hello")
}

// TestSearchHonorsDeletes ensures deleted files are not returned in search.
func TestSearchHonorsDeletes(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 8
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/notes.txt", "hello world", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	_, err = svc.Delete(context.Background(), auth, "proj", "/notes.txt", false)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "hello", "", 5)
	require.NoError(t, err)
	require.Empty(t, searchRes.Chunks)
}

// TestSearchFallbackWhenRerankFails verifies search falls back to fused scores.
func TestSearchFallbackWhenRerankFails(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	svc.rerank = stubRerankClient{err: errors.New("rerank backend unavailable")}
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha match token", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/b.txt", "zzz zzz", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/a.txt", searchRes.Chunks[0].FilePath)
}

// TestSearchPathPrefixFilter verifies search honors raw path prefix filtering.
func TestSearchPathPrefixFilter(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/a.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/other/b.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "match", "/dir", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	for _, chunk := range searchRes.Chunks {
		require.True(t, strings.HasPrefix(chunk.FilePath, "/dir"))
	}
}

// TestSearchUpdatesLastServedOnlyForReturned verifies only final chunks are marked served.
func TestSearchUpdatesLastServedOnlyForReturned(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 4
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello world", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/b.txt", "hello world", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	_, err = svc.Search(context.Background(), auth, "proj", "hello", "", 1)
	require.NoError(t, err)

	var servedCount int64
	err = svc.db.WithContext(context.Background()).
		Model(&FileChunk{}).
		Where("last_served_at IS NOT NULL").
		Count(&servedCount).Error
	require.NoError(t, err)
	require.Equal(t, int64(1), servedCount)
}

// TestSearchTenantIsolation verifies search results are isolated by API key hash.
func TestSearchTenantIsolation(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	_, err := svc.Write(context.Background(), authA, "proj", "/a.txt", "tenant alpha", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authB, "proj", "/b.txt", "tenant bravo", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	resultA, err := svc.Search(context.Background(), authA, "proj", "tenant", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, resultA.Chunks)
	for _, chunk := range resultA.Chunks {
		require.Equal(t, "/a.txt", chunk.FilePath)
	}

	resultB, err := svc.Search(context.Background(), authB, "proj", "tenant", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, resultB.Chunks)
	for _, chunk := range resultB.Chunks {
		require.Equal(t, "/b.txt", chunk.FilePath)
	}
}

// TestSearchFallsBackWhenSemanticBackendFails verifies semantic backend failures degrade to lexical-only retrieval.
func TestSearchFallsBackWhenSemanticBackendFails(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	err = svc.db.WithContext(context.Background()).Exec("UPDATE mcp_file_chunk_embeddings SET embedding = ?", "not-a-valid-embedding").Error
	require.NoError(t, err)

	searchRes, err := svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/a.txt", searchRes.Chunks[0].FilePath)
}
