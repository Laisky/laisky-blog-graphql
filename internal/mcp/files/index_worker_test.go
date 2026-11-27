package files

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/db/sql/kv"
)

// TestIndexWorkerDeletesCredentialAfterSuccess verifies credential envelopes are removed after processing.
func TestIndexWorkerDeletesCredentialAfterSuccess(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello worker", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.Equal(t, 1, credentialEnvelopeCount(t, svc))

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Zero(t, credentialEnvelopeCount(t, svc))
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

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "hello retry", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	dropCredentialEnvelopes(t, svc)

	worker := svc.NewIndexWorker()
	require.NoError(t, worker.RunOnce(context.Background()))

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
	svc := newTestService(t, settings, embedder)
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

// TestPurgeExpiredCredentialsIsBoundedAndThrottled verifies the worker sweep
// deletes only expired envelopes, at most one batch per statement, sweeps
// again while full batches remain, and otherwise waits one credential TTL.
func TestPurgeExpiredCredentialsIsBoundedAndThrottled(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Security.CredentialCacheTTL = time.Minute
	svc := newTestService(t, settings, nil)
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	svc.clock = func() time.Time { return now }
	ctx := context.Background()

	for i := range credentialPurgeBatch + 5 {
		require.NoError(t, svc.credentials.SetWithTTL(ctx, fmt.Sprintf("expired_%d", i), "envelope", time.Minute))
	}
	require.NoError(t, svc.credentials.SetWithTTL(ctx, "live", "envelope", time.Hour))
	now = now.Add(2 * time.Minute)

	svc.purgeExpiredCredentials(ctx)
	require.Equal(t, 6, credentialEnvelopeCount(t, svc), "one sweep deletes at most one batch")
	svc.purgeExpiredCredentials(ctx)
	require.Equal(t, 1, credentialEnvelopeCount(t, svc), "a full batch lets the next pass sweep again")

	require.NoError(t, svc.credentials.SetWithTTL(ctx, "late", "envelope", time.Second))
	now = now.Add(2 * time.Second)
	svc.purgeExpiredCredentials(ctx)
	require.Equal(t, 2, credentialEnvelopeCount(t, svc), "a partial batch waits one TTL before the next sweep")
	now = now.Add(time.Minute)
	svc.purgeExpiredCredentials(ctx)
	require.Equal(t, 1, credentialEnvelopeCount(t, svc))
	_, err := svc.credentials.Get(ctx, "live")
	require.NoError(t, err, "a live envelope survives every sweep")
}

// TestCredentialKeyFitsTheLongestReference verifies the envelope of every
// reference a mutation can produce is storable, whatever bytes its tenant hash,
// project and path hold and however long they are, so no valid mutation can
// fail on its envelope key.
func TestCredentialKeyFitsTheLongestReference(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	svc := newTestService(t, settings, nil)
	longestPath := "/" + strings.Repeat("p", 511)
	longestProject := strings.Repeat("j", 128)
	require.NoError(t, ValidatePath(longestPath))
	require.NoError(t, ValidateProject(longestProject))
	now := time.Now().UTC()
	refs := []CredentialReference{
		{APIKeyHash: strings.Repeat("f", 64), Project: longestProject, Path: longestPath, UpdatedAt: now},
		{APIKeyHash: "user:test", Project: "@DEFAULT", Path: "/notes/a.md", UpdatedAt: now},
		{APIKeyHash: "h", Project: "proj", Path: "/(draft) a+b#1/中文.md", UpdatedAt: now},
	}
	for _, ref := range refs {
		require.NoError(t, svc.credentials.SetWithTTL(context.Background(), ref.credentialKey(), "envelope", time.Minute), "%+v", ref)
	}
	require.Equal(t, len(refs), credentialEnvelopeCount(t, svc), "each reference has its own envelope")
}

// TestCredentialTTLAboveTheStoreMaximumIsClamped verifies a configured
// envelope TTL above the kv maximum cannot make every write fail: the service
// uses the maximum instead.
func TestCredentialTTLAboveTheStoreMaximumIsClamped(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Security.CredentialCacheTTL = 60 * 24 * time.Hour
	svc := newTestService(t, settings, nil)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "kept", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var expireAt time.Time
	require.NoError(t, svc.db.QueryRowContext(context.Background(), "SELECT expire_at FROM kv").Scan(&expireAt))
	require.WithinDuration(t, svc.clock().Add(kv.MaxTTL), expireAt, time.Second)
}
