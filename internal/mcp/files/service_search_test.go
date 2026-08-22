package files

import (
	"context"
	"encoding/json"
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
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = 'UPSERT'
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

// TestSearchWildcardSpansProjects verifies project="*" returns chunks from
// every project owned by the caller and surfaces the per-chunk project field.
func TestSearchWildcardSpansProjects(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj_a", "/a.txt", "alpha sentinel marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_b", "/b.txt", "alpha sentinel marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	res, err := svc.Search(context.Background(), auth, ProjectWildcard, "sentinel", "", 10)
	require.NoError(t, err)
	require.Len(t, res.Chunks, 2)

	projects := map[string]string{}
	for _, chunk := range res.Chunks {
		require.NotEmpty(t, chunk.Project, "wildcard search must populate project on every chunk")
		require.Contains(t, chunk.ChunkContent, "sentinel")
		projects[chunk.Project] = chunk.FilePath
	}
	require.Equal(t, "/a.txt", projects["proj_a"])
	require.Equal(t, "/b.txt", projects["proj_b"])
}

// TestSearchSingleProjectOmitsProjectField guarantees the response shape stays
// backward compatible: non-wildcard searches must not emit the project field.
func TestSearchSingleProjectOmitsProjectField(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj_a", "/a.txt", "alpha sentinel marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_b", "/b.txt", "alpha sentinel marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	res, err := svc.Search(context.Background(), auth, "proj_a", "sentinel", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	for _, chunk := range res.Chunks {
		require.Equal(t, "/a.txt", chunk.FilePath)
		require.Empty(t, chunk.Project, "non-wildcard search must not populate project field")
	}

	encoded, err := json.Marshal(res.Chunks[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "\"project\"")
}

// TestSearchWildcardHonorsTenantIsolation confirms project="*" is still bounded
// by api-key hash and never exposes another tenant's projects.
func TestSearchWildcardHonorsTenantIsolation(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	_, err := svc.Write(context.Background(), authA, "proj_a1", "/a1.txt", "tenant unique alpha", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authA, "proj_a2", "/a2.txt", "tenant unique alpha", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), authB, "proj_b1", "/b1.txt", "tenant unique alpha", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	resA, err := svc.Search(context.Background(), authA, ProjectWildcard, "alpha", "", 10)
	require.NoError(t, err)
	require.Len(t, resA.Chunks, 2)
	for _, chunk := range resA.Chunks {
		require.NotEqual(t, "proj_b1", chunk.Project)
	}

	resB, err := svc.Search(context.Background(), authB, ProjectWildcard, "alpha", "", 10)
	require.NoError(t, err)
	require.Len(t, resB.Chunks, 1)
	require.Equal(t, "proj_b1", resB.Chunks[0].Project)
	require.Equal(t, "/b1.txt", resB.Chunks[0].FilePath)
}

// TestSearchWildcardHonorsPathPrefix verifies path_prefix still applies under
// the cross-project wildcard.
func TestSearchWildcardHonorsPathPrefix(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj_a", "/dir/a.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_b", "/dir/b.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_a", "/other/a.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_b", "/other/b.txt", "match data", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

	res, err := svc.Search(context.Background(), auth, ProjectWildcard, "match", "/dir", 10)
	require.NoError(t, err)
	require.Len(t, res.Chunks, 2)
	for _, chunk := range res.Chunks {
		require.True(t, strings.HasPrefix(chunk.FilePath, "/dir"))
		require.Contains(t, []string{"proj_a", "proj_b"}, chunk.Project)
	}
}

// TestSearchWildcardRawFallbackPopulatesProject ensures the raw-file fallback
// path also surfaces the project for cross-project searches before indexing.
func TestSearchWildcardRawFallbackPopulatesProject(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj_a", "/a.txt", "MCP raw fallback marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj_b", "/b.txt", "MCP raw fallback marker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	res, err := svc.Search(context.Background(), auth, ProjectWildcard, "MCP", "", 5)
	require.NoError(t, err)
	require.Len(t, res.Chunks, 2)
	projects := map[string]struct{}{}
	for _, chunk := range res.Chunks {
		require.NotEmpty(t, chunk.Project, "raw-fallback wildcard chunks must include project")
		projects[chunk.Project] = struct{}{}
	}
	require.Contains(t, projects, "proj_a")
	require.Contains(t, projects, "proj_b")
}

// TestSearchWildcardEmptyAuthRejected guards against bypassing tenant checks
// via the wildcard.
func TestSearchWildcardEmptyAuthRejected(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})

	_, err := svc.Search(context.Background(), AuthContext{}, ProjectWildcard, "anything", "", 5)
	require.Error(t, err)
}

// TestNonSearchOperationsRejectWildcard confirms only Search accepts "*" and
// every other file operation refuses it so callers cannot accidentally
// stat/read/write/delete/rename/list across projects.
func TestNonSearchOperationsRejectWildcard(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	wildcard := ProjectWildcard

	t.Run("Stat", func(t *testing.T) {
		_, err := svc.Stat(context.Background(), auth, wildcard, "/x.txt")
		require.Error(t, err)
		var fileErr *Error
		require.True(t, errors.As(err, &fileErr))
		require.Equal(t, ErrCodeInvalidPath, fileErr.Code)
	})

	t.Run("Read", func(t *testing.T) {
		_, err := svc.Read(context.Background(), auth, wildcard, "/x.txt", 0, 0)
		require.Error(t, err)
	})

	t.Run("Write", func(t *testing.T) {
		_, err := svc.Write(context.Background(), auth, wildcard, "/x.txt", "data", "utf-8", 0, WriteModeAppend)
		require.Error(t, err)
	})

	t.Run("Delete", func(t *testing.T) {
		_, err := svc.Delete(context.Background(), auth, wildcard, "/x.txt", false)
		require.Error(t, err)
	})

	t.Run("Rename", func(t *testing.T) {
		_, err := svc.Rename(context.Background(), auth, wildcard, "/a.txt", "/b.txt", false)
		require.Error(t, err)
	})

	t.Run("List", func(t *testing.T) {
		_, err := svc.List(context.Background(), auth, wildcard, "", 1, 10)
		require.Error(t, err)
	})
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
