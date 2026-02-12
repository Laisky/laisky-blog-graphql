package files

import (
	"context"
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

// TestIndexWorkerRetriesWhenCredentialMissing verifies missing envelopes reschedule jobs with retry.
func TestIndexWorkerRetriesWhenCredentialMissing(t *testing.T) {
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
	err = svc.db.WithContext(context.Background()).
		Where("apikey_hash = ? AND project = ? AND file_path = ?", auth.APIKeyHash, "proj", "/a.txt").
		First(&job).Error
	require.NoError(t, err)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, 1, job.RetryCount)
}
