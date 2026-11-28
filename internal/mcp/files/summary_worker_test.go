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
func newSummaryTestService(t *testing.T, stub FileSummarizer) (*Service, func(time.Time)) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.Index.BatchSize = 10
	settings.Index.RetryMax = 2
	settings.Index.RetryBackoff = time.Second
	settings.Index.ChunkBytes = 64
	settings.MaxProjectBytes = 1_000_000

	svc := newTestService(t, settings, testEmbedder{vector: pgvector.NewVector([]float32{1, 0})})
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
	return svc, setClock
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
	svc, _ := newSummaryTestService(t, stub)
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
	svc, _ := newSummaryTestService(t, stub)
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
	svc, _ := newSummaryTestService(t, stub)
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
	svc, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/old.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount())

	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "proj", FromPath: "/old.txt", ToPath: "/new.txt"})
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount(), "rename must not call the summarizer")

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	require.Equal(t, "/new.txt", res.Chunks[0].FilePath)
	require.Equal(t, stub.result, res.Chunks[0].FileSummary)
}

// TestSummaryGenerationKeyChangeRegenerates covers §5.1: changing summary
// generation inputs must not reuse a ready summary from the old configuration.
func TestSummaryGenerationKeyChangeRegenerates(t *testing.T) {
	stub := &stubFileSummarizer{result: "The original generation summary."}
	svc, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 1, stub.callCount())

	svc.settings.Index.FileSummary.PromptVersion = "changed-prompt"
	stub.mu.Lock()
	stub.result = "The regenerated summary for the changed prompt."
	stub.mu.Unlock()
	_, err = svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, 2, stub.callCount())

	summary, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
	require.Equal(t, "The regenerated summary for the changed prompt.", summary)
}

// TestSummarySearchEnforcesSummary covers the hard enforcement contract: an
// indexed chunk whose summary is missing or generation-mismatched is excluded,
// and raw fallback must not reintroduce it.
func TestSummarySearchEnforcesSummary(t *testing.T) {
	stub := &stubFileSummarizer{result: "A valid summary for the searchable document."}
	svc, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))

	_, err = svc.db.ExecContext(ctx,
		`UPDATE mcp_files SET file_summary = '', summary_content_hash = '', summary_status = 'pending'
		WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE`,
		"hash", "proj", "/a.txt")
	require.NoError(t, err)
	svc.settings.Search.EnforceSummary = true

	res, err := svc.Search(ctx, auth, "proj", "alpha", "", 10)
	require.NoError(t, err)
	require.Empty(t, res.Chunks)
}

// TestSummaryProviderFailurePublishesDegraded covers F01: a provider failure publishes
// a deterministic fallback (degraded) and enqueues a bounded refresh; write is durable.
func TestSummaryProviderFailurePublishesDegraded(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, _ := newSummaryTestService(t, stub)
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
	svc, _ := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	dropCredentialEnvelopes(t, svc)

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
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta retry", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status)

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
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))

	dropCredentialEnvelopes(t, svc) // no credential for the refresh
	setClock(base.Add(10 * time.Second))
	require.NoError(t, worker.RunOnce(ctx))

	var jobStatus, errCode string
	require.NoError(t, svc.db.QueryRowContext(ctx,
		`SELECT status, last_error_code FROM mcp_file_index_jobs WHERE operation = 'SUMMARY_REFRESH' ORDER BY id DESC LIMIT 1`).Scan(&jobStatus, &errCode))
	require.Equal(t, "waiting_auth", jobStatus)
	require.Equal(t, "credential_unavailable", errCode)

	// An authenticated no-op full replacement retains the generation and
	// reactivates the parked job with a fresh envelope ID.
	stub.mu.Lock()
	stub.err = nil
	stub.result = "A validated summary after re-authentication."
	stub.mu.Unlock()
	_, err = svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))

	summary, status, _, _, source := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
	require.Equal(t, string(SummarySourceModel), source)
	require.Equal(t, "A validated summary after re-authentication.", summary)
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
	svc, _ := newSummaryTestService(t, contentEchoSummarizer{})
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
	svc, _ := newSummaryTestService(t, stub)
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

// TestSummaryRefreshFollowsARenamedDegradedFile verifies a degraded summary
// that moves with its file keeps a refresh eligible at the new path: the
// refresh queued for the old path ends without a file, and the destination's
// index job, which reuses the degraded summary, queues one for the new path.
func TestSummaryRefreshFollowsARenamedDegradedFile(t *testing.T) {
	for _, project := range []string{"proj", "archive"} {
		t.Run(project, func(t *testing.T) { checkSummaryRefreshFollowsARename(t, project) })
	}
}

func checkSummaryRefreshFollowsARename(t *testing.T, target string) {
	t.Helper()
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	worker := svc.NewIndexWorker()

	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "alpha beta gamma delta moved", "utf-8", 0, WriteModeAppend)
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status)

	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "proj", FromPath: "/a.txt", TargetProject: target, ToPath: "/b.txt"})
	require.NoError(t, err)
	require.NoError(t, worker.RunOnce(ctx))
	_, status, _, _, _ = summaryRow(t, svc, "hash", target, "/b.txt")
	require.Equal(t, string(SummaryStatusDegraded), status, "the move reuses the degraded summary")

	stub.mu.Lock()
	stub.err = nil
	stub.result = "A validated model summary of the moved file."
	stub.mu.Unlock()
	setClock(base.Add(10 * time.Second))
	require.NoError(t, worker.RunOnce(ctx))

	summary, status, _, _, source := summaryRow(t, svc, "hash", target, "/b.txt")
	require.Equal(t, string(SummaryStatusReady), status, "a refresh stays eligible at the new path")
	require.Equal(t, string(SummarySourceModel), source)
	require.Equal(t, "A validated model summary of the moved file.", summary)
}

// TestFreshCredentialReachesClaimedRefresh preserves a new authorization when
// one worker is holding an expired reference and another supplies fresh auth.
func TestFreshCredentialReachesClaimedRefresh(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	const content = "The same content is available during fresh reauthorization."
	_, err := svc.Write(ctx, auth, "proj", "/a.txt", content, "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	first := svc.NewIndexWorker()
	require.NoError(t, first.RunOnce(ctx))
	setClock(time.Date(2026, 2, 11, 0, 0, 10, 0, time.UTC))
	claimed, err := first.claimJobs(ctx)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "SUMMARY_REFRESH", claimed[0].Operation)
	require.NotEmpty(t, claimed[0].CredentialID)
	dropCredentialEnvelopes(t, svc)
	_, err = svc.Write(ctx, auth, "proj", "/a.txt", content, "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, first.processJob(ctx, claimed[0]))
	var pending, currentID string
	require.NoError(t, svc.db.QueryRowContext(ctx, "SELECT status, credential_id FROM mcp_file_index_jobs WHERE id = ?", claimed[0].ID).Scan(&pending, &currentID))
	require.Equal(t, indexStatusPending, pending)
	require.NotEqual(t, claimed[0].CredentialID, currentID)

	stub.mu.Lock()
	stub.err = nil
	stub.result = "A current document summary using fresh caller authorization."
	stub.mu.Unlock()
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
}

// TestFreshCredentialReachesPendingRefresh renews authorization before the
// already queued refresh is claimed, without changing its retry schedule.
func TestFreshCredentialReachesPendingRefresh(t *testing.T) {
	stub := &stubFileSummarizer{err: errTestProvider}
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx := context.Background()
	const content = "The pending refresh still has the exact same file content."
	_, err := svc.Write(ctx, auth, "proj", "/a.txt", content, "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	var oldID string
	var availableAt time.Time
	require.NoError(t, svc.db.QueryRowContext(ctx, "SELECT credential_id, available_at FROM mcp_file_index_jobs WHERE operation = 'SUMMARY_REFRESH' AND status = 'pending'").Scan(&oldID, &availableAt))
	_, err = svc.db.ExecContext(ctx, "UPDATE kv SET expire_at = ?", time.Unix(1, 0).UTC())
	require.NoError(t, err)
	_, err = svc.Write(ctx, auth, "proj", "/a.txt", content, "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	var currentID string
	var unchangedAt time.Time
	require.NoError(t, svc.db.QueryRowContext(ctx, "SELECT credential_id, available_at FROM mcp_file_index_jobs WHERE operation = 'SUMMARY_REFRESH' AND status = 'pending'").Scan(&currentID, &unchangedAt))
	require.True(t, unchangedAt.Equal(availableAt))
	require.NotEqual(t, oldID, currentID)
	stub.err = nil
	stub.result = "Fresh authorization allows the pending summary to be upgraded."
	setClock(availableAt.Add(time.Second))
	require.NoError(t, svc.NewIndexWorker().RunOnce(ctx))
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status)
}

// testSummaryFunc permits deterministic scheduling of the model call in a fixture.
type testSummaryFunc func(context.Context, string, string) (string, error)

// GenerateFileSummary implements FileSummarizer.
func (fn testSummaryFunc) GenerateFileSummary(ctx context.Context, key, doc string) (string, error) {
	return fn(ctx, key, doc)
}

// TestSummaryRefreshSurvivesRenameRoundTrip verifies that an in-flight refresh
// cannot disappear when a rename advances the same file's revision twice.
func TestSummaryRefreshSurvivesRenameRoundTrip(t *testing.T) {
	started, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	var mu sync.Mutex
	calls := 0
	stub := testSummaryFunc(func(ctx context.Context, _, _ string) (string, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		switch call {
		case 1:
			return "", errTestProvider
		case 2:
			close(started)
			select {
			case <-resume:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "A ready summary of the unchanged document after its two moves.", nil
	})
	svc, setClock := newSummaryTestService(t, stub)
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	first := svc.NewIndexWorker()
	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "The unchanged document completes a round trip.", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	require.NoError(t, first.RunOnce(ctx))
	setClock(base.Add(10 * time.Second))
	done := make(chan error, 1)
	go func() { done <- first.RunOnce(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		require.FailNow(t, "refresh did not enter the model call")
	}
	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "proj", FromPath: "/a.txt", ToPath: "/b.txt"})
	require.NoError(t, err)
	setClock(base.Add(11 * time.Second))
	_, err = svc.Rename(ctx, auth, RenameRequest{Project: "proj", FromPath: "/b.txt", ToPath: "/a.txt"})
	require.NoError(t, err)
	second := svc.NewIndexWorker()
	require.NoError(t, second.RunOnce(ctx))
	release.Do(func() { close(resume) })
	require.NoError(t, <-done)
	_, status, _, _, _ := summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusDegraded), status, "the stale revision never publishes")
	setClock(base.Add(20 * time.Second))
	require.NoError(t, second.RunOnce(ctx))
	_, status, _, _, _ = summaryRow(t, svc, "hash", "proj", "/a.txt")
	require.Equal(t, string(SummaryStatusReady), status, "the bounded retry publishes only after reloading the revision")
}
