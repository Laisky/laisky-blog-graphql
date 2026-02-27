package files

import (
	"context"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// TestIndexWorkerDeletesCredentialAfterSuccess verifies credential envelopes are removed after processing.
func TestIndexWorkerDeletesCredentialAfterSuccess(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	store := &memoryCredentialStore{}
	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, store)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello worker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NotEmpty(t, store.data)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Empty(t, store.data)
}

// TestIndexWorkerDegradesWhenCredentialMissing verifies missing envelopes degrade to lexical indexing without retries.
func TestIndexWorkerDegradesWhenCredentialMissing(t *testing.T) {
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

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello retry", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	store.data = map[string]string{}

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

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

	var chunkCount int
	err = svc.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(1) FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ?`,
		auth.APIKeyHash,
		"proj",
		"/a.txt",
	).Scan(&chunkCount)
	require.NoError(t, err)
	require.Greater(t, chunkCount, 0)
}

// TestIndexWorkerUsesContextualizedInputs verifies indexing prepends generated context before embedding.
func TestIndexWorkerUsesContextualizedInputs(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	capturedInputs := []string{}
	embedder := testEmbedder{
		vector:       pgvector.NewVector([]float32{1, 0}),
		inputCapture: &capturedInputs,
	}
	store := &memoryCredentialStore{}
	svc := newTestService(t, settings, embedder, store)
	svc.contextualizer = testContextualizer{contexts: []string{"context-for-chunk"}}
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))
	require.NotEmpty(t, capturedInputs)
	require.True(t, strings.Contains(capturedInputs[0], "context-for-chunk"))
	require.True(t, strings.Contains(capturedInputs[0], "alpha beta gamma delta"))
}
