package files

import (
	"context"
	"sort"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// listIndexJobs returns the persisted index jobs ordered for deterministic assertions.
func listIndexJobs(t *testing.T, svc *Service) []FileIndexJob {
	t.Helper()

	rows, err := svc.db.QueryContext(
		context.Background(),
		`SELECT id, apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at
		FROM mcp_file_index_jobs
		ORDER BY id ASC`,
	)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	jobs := []FileIndexJob{}
	for rows.Next() {
		var job FileIndexJob
		err = rows.Scan(
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
		jobs = append(jobs, job)
	}
	require.NoError(t, rows.Err())
	return jobs
}

// summarizeJobs groups jobs by operation and path for concise assertions.
func summarizeJobs(jobs []FileIndexJob) []string {
	summary := make([]string, 0, len(jobs))
	for _, job := range jobs {
		summary = append(summary, job.Operation+":"+job.FilePath)
	}
	sort.Strings(summary)
	return summary
}

// TestWriteDeleteAndRenamePersistExpectedIndexJobs verifies each mutation persists the expected outbox jobs.
func TestWriteDeleteAndRenamePersistExpectedIndexJobs(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 20
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "aaa", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/docs/sub/b.txt", "bbb", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Delete(context.Background(), auth, "proj", "/docs/sub", true)
	require.NoError(t, err)
	_, err = svc.Rename(context.Background(), auth, "proj", "/docs/a.txt", "/archive/a.txt", false)
	require.NoError(t, err)

	jobs := listIndexJobs(t, svc)
	require.Equal(t, []string{
		"DELETE:/docs/a.txt",
		"DELETE:/docs/sub/b.txt",
		"UPSERT:/archive/a.txt",
		"UPSERT:/docs/a.txt",
		"UPSERT:/docs/sub/b.txt",
	}, summarizeJobs(jobs))

	for _, job := range jobs {
		require.Equal(t, auth.APIKeyHash, job.APIKeyHash)
		require.Equal(t, "proj", job.Project)
		require.Equal(t, "pending", job.Status)
		require.Zero(t, job.RetryCount)
		require.NotNil(t, job.FileUpdatedAt)
	}
}

// TestRenameDirectoryEnqueuesPerFileJobs verifies directory moves emit delete and upsert jobs for every moved file.
func TestRenameDirectoryEnqueuesPerFileJobs(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 20
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 10_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, &memoryCredentialStore{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/docs/a.txt", "aaa", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/docs/sub/b.txt", "bbb", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	_, err = svc.Rename(context.Background(), auth, "proj", "/docs", "/archive", false)
	require.NoError(t, err)

	jobs := summarizeJobs(listIndexJobs(t, svc))
	require.Equal(t, []string{
		"DELETE:/docs/a.txt",
		"DELETE:/docs/sub/b.txt",
		"UPSERT:/archive/a.txt",
		"UPSERT:/archive/sub/b.txt",
		"UPSERT:/docs/a.txt",
		"UPSERT:/docs/sub/b.txt",
	}, jobs)
	for _, expected := range []string{"DELETE:/docs/a.txt", "DELETE:/docs/sub/b.txt", "UPSERT:/archive/a.txt", "UPSERT:/archive/sub/b.txt"} {
		require.Contains(t, jobs, expected)
	}
}
