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

type captureRerankClient struct {
	keys []string
}

// Rerank captures the API key used for rerank requests.
func (c *captureRerankClient) Rerank(_ context.Context, apiKey, _ string, docs []string) ([]float64, error) {
	c.keys = append(c.keys, apiKey)
	scores := make([]float64, len(docs))
	for i := range docs {
		scores[i] = float64(len(docs) - i)
	}
	return scores, nil
}

type captureEmbedder struct {
	keys   []string
	vector pgvector.Vector
}

// EmbedTexts captures the API key used for embedding requests.
func (c *captureEmbedder) EmbedTexts(_ context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error) {
	c.keys = append(c.keys, apiKey)
	result := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		result = append(result, c.vector)
	}
	return result, nil
}

// Rerank returns configured rerank scores or an error.
func (s stubRerankClient) Rerank(context.Context, string, string, []string) ([]float64, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.scores, nil
}

// TestSearchUsesRequestAPIKeyForExternalCalls verifies file_search forwards the request API key to external embedding/rerank calls.
func TestSearchUsesRequestAPIKeyForExternalCalls(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	embedder := &captureEmbedder{vector: pgvector.NewVector([]float32{1, 0})}
	reranker := &captureRerankClient{}
	svc := newTestService(t, settings, embedder, &memoryCredentialStore{})
	svc.rerank = reranker
	auth := AuthContext{APIKeyHash: "hash-tenant", APIKey: "tenant-user-key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	_, err = svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)

	require.NotEmpty(t, embedder.keys)
	for _, key := range embedder.keys {
		require.Equal(t, auth.APIKey, key)
	}
	require.NotEmpty(t, reranker.keys)
	for _, key := range reranker.keys {
		require.Equal(t, auth.APIKey, key)
	}
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
	require.False(t, searchRes.Chunks[0].IsFullFile)
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
	err = svc.db.QueryRowContext(context.Background(), "SELECT COUNT(1) FROM mcp_file_chunks WHERE last_served_at IS NOT NULL").Scan(&servedCount)
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

	_, err = svc.db.ExecContext(context.Background(), "UPDATE mcp_file_chunk_embeddings SET embedding = ?", "not-a-valid-embedding")
	require.NoError(t, err)

	searchRes, err := svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/a.txt", searchRes.Chunks[0].FilePath)
}

// TestSearchKeepsLexicalResultsWhenEmbeddingFails verifies lexical indexing survives embedder failures.
func TestSearchKeepsLexicalResultsWhenEmbeddingFails(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.RetryMax = 2
	settings.Index.RetryBackoff = 0
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, errTestEmbedder{}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/a.txt", searchRes.Chunks[0].FilePath)

	var chunkCount int64
	err = svc.db.QueryRowContext(context.Background(), "SELECT COUNT(1) FROM mcp_file_chunks").Scan(&chunkCount)
	require.NoError(t, err)
	require.Greater(t, chunkCount, int64(0))

	var embeddingCount int64
	err = svc.db.QueryRowContext(context.Background(), "SELECT COUNT(1) FROM mcp_file_chunk_embeddings").Scan(&embeddingCount)
	require.NoError(t, err)
	require.Equal(t, int64(0), embeddingCount)
}

// TestSearchKeepsLexicalResultsWhenCredentialMissing verifies lexical indexing is available while semantic retries.
func TestSearchKeepsLexicalResultsWhenCredentialMissing(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.RetryMax = 2
	settings.Index.RetryBackoff = 0
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	store := &memoryCredentialStore{}
	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, store)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	store.data = map[string]string{}

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	searchRes, err := svc.Search(context.Background(), auth, "proj", "alpha", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/a.txt", searchRes.Chunks[0].FilePath)

	var job FileIndexJob
	err = svc.db.QueryRowContext(
		context.Background(),
		`SELECT id, apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at
		FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND project = ? AND file_path = ?
		ORDER BY id DESC
		LIMIT 1`,
		auth.APIKeyHash,
		"proj",
		"/a.txt",
	).Scan(
		&job.ID,
		&job.APIKeyHash,
		&job.Project,
		&job.FilePath,
		&job.Operation,
		&job.FileUpdatedAt,
		&job.Status,
		&job.RetryCount,
		&job.AvailableAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	require.NoError(t, err)
	require.Equal(t, "done", job.Status)
	require.Equal(t, 0, job.RetryCount)
}

// TestSearchFallbackReturnsResultsBeforeIndexReady verifies write-then-search works even before index workers process jobs.
func TestSearchFallbackReturnsResultsBeforeIndexReady(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/notes.txt", "MCP fallback should find this text", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	searchRes, err := svc.Search(context.Background(), auth, "proj", "MCP", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	require.Equal(t, "/notes.txt", searchRes.Chunks[0].FilePath)
	require.Contains(t, strings.ToLower(searchRes.Chunks[0].ChunkContent), "mcp")
	require.True(t, searchRes.Chunks[0].IsFullFile)
}

// TestSearchFallbackHonorsPathPrefix verifies raw-file fallback respects path prefix filtering.
func TestSearchFallbackHonorsPathPrefix(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/dir/a.txt", "contains MCP marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/other/b.txt", "contains MCP marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	searchRes, err := svc.Search(context.Background(), auth, "proj", "MCP", "/dir", 5)
	require.NoError(t, err)
	require.NotEmpty(t, searchRes.Chunks)
	for _, chunk := range searchRes.Chunks {
		require.True(t, strings.HasPrefix(chunk.FilePath, "/dir"))
	}
}
