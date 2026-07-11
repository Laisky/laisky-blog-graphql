package files

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// errTestProvider simulates a transient summarization provider failure.
var errTestProvider = errors.New("summary provider unavailable")

// stubFileSummarizer is a deterministic FileSummarizer for tests. It records call
// count and the last document it was asked to summarize.
type stubFileSummarizer struct {
	mu      sync.Mutex
	calls   int
	lastDoc string
	result  string
	err     error
}

func (s *stubFileSummarizer) GenerateFileSummary(_ context.Context, _ string, doc string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastDoc = doc
	if s.err != nil {
		return "", s.err
	}
	return s.result, nil
}

func (s *stubFileSummarizer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubFileSummarizer) doc() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDoc
}

// newSummaryTestService builds a service wired with the stub summarizer and a mutable
// clock so refresh-job availability windows can be advanced deterministically.
func newSummaryTestService(t *testing.T, stub FileSummarizer) (*Service, *memoryCredentialStore, func(time.Time)) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.RetryMax = 2
	settings.Index.RetryBackoff = time.Second
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 1_000_000

	store := &memoryCredentialStore{}
	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})}, store)
	svc.summarizer = stub
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	current := base
	var mu sync.Mutex
	svc.clock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	setClock := func(ts time.Time) {
		mu.Lock()
		defer mu.Unlock()
		current = ts
	}
	return svc, store, setClock
}

func summaryRow(t *testing.T, svc *Service, apiKeyHash, project, path string) (summary, status, contentHash, summaryHash, source string) {
	t.Helper()
	err := svc.db.QueryRowContext(context.Background(),
		`SELECT file_summary, summary_status, content_hash, summary_content_hash, summary_source
		FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`,
		apiKeyHash, project, path,
	).Scan(&summary, &status, &contentHash, &summaryHash, &source)
	require.NoError(t, err)
	return
}

func countRefreshJobs(t *testing.T, svc *Service, apiKeyHash, project, path string) int {
	t.Helper()
	var n int
	err := svc.db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM mcp_file_index_jobs WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = 'SUMMARY_REFRESH'`,
		apiKeyHash, project, path,
	).Scan(&n)
	require.NoError(t, err)
	return n
}

// TestSummarySearchHitIncludesSummary covers C01: every hit carries a non-empty
// file_summary bound to the same generation as the chunk.
func TestSummarySearchHitIncludesSummary(t *testing.T) {
	stub := &stubFileSummarizer{result: "This document describes the retry queue saturation incident and its mitigation timeline."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta epsilon", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	for _, c := range res.Chunks {
		require.Equal(t, stub.result, c.FileSummary)
	}
	require.Equal(t, 1, stub.callCount())

	_, status, contentHash, summaryHash, source := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
	require.Equal(t, string(SummarySourceModel), source)
	require.Equal(t, contentHash, summaryHash, "chunk and summary must share a content generation")
}

// TestSummaryRepeatedAcrossChunks covers C02: multiple chunks from one file repeat
// the same summary with independent offsets.
func TestSummaryRepeatedAcrossChunks(t *testing.T) {
	stub := &stubFileSummarizer{result: "A grounded overview of the alpha document used across many chunks."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	content := strings.Repeat("alpha beta gamma delta epsilon zeta ", 20)
	_, err := svc.Write(ctx, auth, "proj", "/big.txt", content, "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 20)
	require.NoError(t, err)
	require.Greater(t, len(res.Chunks), 1, "content should split into multiple chunks")
	offsets := map[int64]bool{}
	for _, c := range res.Chunks {
		require.Equal(t, stub.result, c.FileSummary)
		offsets[c.FileSeekStartBytes] = true
	}
	require.Greater(t, len(offsets), 1, "chunks must have independent offsets")
}

// TestSummaryAppendSummarizesCompleteFile covers G4/M02: APPEND summarizes the whole
// resulting file, not only the appended bytes.
func TestSummaryAppendSummarizesCompleteFile(t *testing.T) {
	stub := &stubFileSummarizer{result: "A valid grounded summary sentence."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "part-one-content ", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount())

	_, err = svc.Write(ctx, auth, "proj", "/a.txt", "part-two-content", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 2, stub.callCount())

	doc := stub.doc()
	require.Contains(t, doc, "part-one-content")
	require.Contains(t, doc, "part-two-content", "summary input must be the complete post-append file")
}

// TestSummaryRenameReusesWithoutModelCall covers G5/M06 and §8.8: a content-preserving
// rename reuses the summary and makes zero summary-provider calls.
func TestSummaryRenameReusesWithoutModelCall(t *testing.T) {
	stub := &stubFileSummarizer{result: "A grounded overview reused across the rename."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/old.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount())

	_, err = svc.Rename(ctx, auth, "proj", "/old.txt", "/new.txt", false)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount(), "rename must not call the summarizer")

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	require.Equal(t, "/new.txt", res.Chunks[0].FilePath)
	require.Equal(t, stub.result, res.Chunks[0].FileSummary)
}

// TestSummaryProviderFailurePublishesDegraded covers F01: a provider failure publishes
// a deterministic fallback (degraded) and enqueues a bounded refresh; write is durable.
func TestSummaryProviderFailurePublishesDegraded(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta retry queue", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	require.Equal(t, 1, stub.callCount())

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	require.NotEmpty(t, res.Chunks[0].FileSummary, "degraded hit still carries a summary")

	summary, status, _, _, source := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status)
	require.Equal(t, string(SummarySourceDeterministicFallback), source)
	require.NotEmpty(t, summary)
	require.Equal(t, 1, countRefreshJobs(t, svc, "hash", "proj", "/a.txt"))
}

// TestSummaryMissingCredentialUsesFallback covers F02: no platform key is used; the
// fallback remains available and the summarizer is never called.
func TestSummaryMissingCredentialUsesFallback(t *testing.T) {
	stub := &stubFileSummarizer{result: "should not be used"}
	svc, store, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	store.data = map[string]string{} // drop the credential envelope

	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	require.Equal(t, 0, stub.callCount(), "no summarizer call without a caller credential")

	_, status, _, _, source := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status)
	require.Equal(t, string(SummarySourceDeterministicFallback), source)
}

// TestSummaryRefreshUpgradesDegradedToReady covers F06/§4.6: an authenticated refresh
// upgrades a degraded fallback to a validated model summary.
func TestSummaryRefreshUpgradesDegradedToReady(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, _, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta retry", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status)

	// Re-authenticate: attach a fresh credential envelope keyed by the original write
	// time (the refresh job carries that file_updated_at), then let the provider succeed.
	require.NoError(t, svc.storeCredentialEnvelope(ctx, auth, "proj", "/a.txt", base))
	stub.mu.Lock()
	stub.err = nil
	stub.result = "A validated model summary describing the retry queue incident."
	stub.mu.Unlock()

	setClock(base.Add(10 * time.Second)) // make the refresh job available
	require.NoError(t, worker.RunOnce(ctx))

	summary, status, _, _, source := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
	require.Equal(t, string(SummarySourceModel), source)
	require.Equal(t, "A validated model summary describing the retry queue incident.", summary)

	var jobStatus string
	require.NoError(t, svc.db.QueryRowContext(ctx,
		`SELECT status FROM mcp_file_index_jobs WHERE operation = 'SUMMARY_REFRESH' ORDER BY id DESC LIMIT 1`).Scan(&jobStatus))
	require.Equal(t, "done", jobStatus)
}

// TestSummaryRefreshWaitsForAuth covers §4.6: without a credential the refresh job
// parks in waiting_auth instead of hot-looping.
func TestSummaryRefreshWaitsForAuth(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, store, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))

	store.data = map[string]string{} // no credential for the refresh
	setClock(base.Add(10 * time.Second))
	require.NoError(t, worker.RunOnce(ctx))

	var jobStatus, errCode string
	require.NoError(t, svc.db.QueryRowContext(ctx,
		`SELECT status, last_error_code FROM mcp_file_index_jobs WHERE operation = 'SUMMARY_REFRESH' ORDER BY id DESC LIMIT 1`).Scan(&jobStatus, &errCode))
	require.Equal(t, "waiting_auth", jobStatus)
	require.Equal(t, "credential_unavailable", errCode)
}

// contentEchoSummarizer derives its summary from the document's first token so tests
// can distinguish per-tenant content.
type contentEchoSummarizer struct{}

func (contentEchoSummarizer) GenerateFileSummary(_ context.Context, _ string, doc string) (string, error) {
	tokens := tokenize(doc)
	head := "document"
	if len(tokens) > 0 {
		head = tokens[0]
	}
	return "This file covers " + head + " material in detail.", nil
}

// TestSummaryTenantIsolation covers S01/A6: a tenant never receives another owner's
// summary for the same project/path.
func TestSummaryTenantIsolation(t *testing.T) {
	svc, _, _ := newSummaryTestService(t, contentEchoSummarizer{})
	ctx := context.Background()
	worker := svc.NewIndexWorker()
	authA := AuthContext{APIKeyHash: "tenantA", APIKey: "kA", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "tenantB", APIKey: "kB", UserIdentity: "user:b"}

	_, err := svc.Write(ctx, authA, "proj", "/s.txt", "alpha alpha alpha secret", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	_, err = svc.Write(ctx, authB, "proj", "/s.txt", "beta beta beta public", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))

	// Tenant B must never see tenant A's content or summary.
	resB, err := svc.Search(ctx, authB, "proj", "alpha", "", 10)
	require.NoError(t, err)
	for _, c := range resB.Chunks {
		require.NotContains(t, strings.ToLower(c.FileSummary), "alpha")
		require.NotContains(t, strings.ToLower(c.ChunkContent), "alpha")
	}

	// Tenant A sees only its own summary.
	resA, err := svc.Search(ctx, authA, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, resA.Chunks)
	for _, c := range resA.Chunks {
		require.Contains(t, strings.ToLower(c.FileSummary), "alpha")
	}
}

// TestSummaryStaleJobDiscardedByContentHash covers §4.1/M09: a job whose expected
// content hash no longer matches the active file is discarded without clobbering the
// current generation.
func TestSummaryStaleJobDiscardedByContentHash(t *testing.T) {
	stub := &stubFileSummarizer{result: "Summary of the current v2 content generation."}
	svc, _, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "v1 alpha content", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	staleHash := HashFileContent([]byte("v1 alpha content"))

	_, err = svc.Write(ctx, auth, "proj", "/a.txt", "v2 beta content", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx)) // publishes v2 ready
	callsAfterV2 := stub.callCount()

	_, status, contentHash, summaryHash, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
	require.Equal(t, contentHash, summaryHash)

	// A stale worker carrying the v1 hash must be discarded before any model call.
	staleJob := FileIndexJob{APIKeyHash: "hash", Project: "proj", FilePath: "/a.txt", Operation: "UPSERT", ContentHash: staleHash}
	require.NoError(t, svc.processUpsertJob(ctx, staleJob))
	require.Equal(t, callsAfterV2, stub.callCount(), "stale job must not call the summarizer")

	summary2, _, _, summaryHash2, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, summaryHash, summaryHash2, "current generation summary must be preserved")
	require.Equal(t, "Summary of the current v2 content generation.", summary2)
}
