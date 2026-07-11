package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pgvector/pgvector-go"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// embeddingPlan describes semantic vectors availability for one indexing attempt.
type embeddingPlan struct {
	vectors []pgvector.Vector
	err     error
}

// IndexWorker processes file indexing jobs.
type IndexWorker struct {
	svc    *Service
	logger logSDK.Logger
}

// StartIndexWorkers starts the configured number of indexing workers.
func (s *Service) StartIndexWorkers(ctx context.Context) error {
	if s == nil {
		return errors.New("file service is nil")
	}
	count := s.settings.Index.Workers
	if count <= 0 {
		return nil
	}
	for i := 0; i < count; i++ {
		worker := s.NewIndexWorker()
		go func() {
			if err := worker.Start(ctx); err != nil {
				worker.logger.Warn("index worker stopped", zap.Error(err))
			}
		}()
	}
	return nil
}

// NewIndexWorker constructs a new index worker instance.
func (s *Service) NewIndexWorker() *IndexWorker {
	logger := s.logger
	if logger == nil {
		logger = log.Logger.Named("mcp_files_index_worker")
	}
	return &IndexWorker{svc: s, logger: logger.Named("worker")}
}

// Start runs the worker loop until the context is canceled.
func (w *IndexWorker) Start(ctx context.Context) error {
	if w == nil || w.svc == nil {
		return errors.New("worker is not configured")
	}
	interval := 500 * time.Millisecond
	for {
		if isContextDone(ctx) {
			return nil
		}
		if err := w.RunOnce(ctx); err != nil {
			w.logger.Warn("index worker run failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// RunOnce processes a batch of pending jobs.
func (w *IndexWorker) RunOnce(ctx context.Context) error {
	jobs, err := w.claimJobs(ctx)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	for _, job := range jobs {
		if err := w.processJob(ctx, job); err != nil {
			w.logger.Warn("process index job failed", zap.Error(err), zap.Int64("job_id", job.ID))
		}
	}
	return nil
}

// claimJobs selects and marks pending index jobs for processing.
func (w *IndexWorker) claimJobs(ctx context.Context) ([]FileIndexJob, error) {
	svc := w.svc
	now := svc.clock()
	batch := svc.settings.Index.BatchSize
	if batch <= 0 {
		batch = 10
	}

	jobs := make([]FileIndexJob, 0, batch)
	tx, err := svc.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "begin claim jobs transaction")
	}

	// Index workers only process user-namespace jobs. System-owner writes never
	// enqueue, but we filter defensively so a future bug cannot leak system rows
	// into the chunking/embedding pipeline.
	query := "SELECT id, apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at, content_hash, last_error_code, summary_generation_key " +
		"FROM mcp_file_index_jobs WHERE status = ? AND available_at <= ? AND system_owner = ? ORDER BY id ASC LIMIT ?"
	args := []any{"pending", now, "", batch}
	if svc.isPostgres {
		query += " FOR UPDATE SKIP LOCKED"
	}

	rows, err := tx.QueryContext(ctx, rebindSQL(query, svc.isPostgres), args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, errors.Wrap(err, "claim index jobs")
	}
	defer rows.Close() //nolint:errcheck // rows closed explicitly below; defer is safety net
	for rows.Next() {
		var job FileIndexJob
		if scanErr := rows.Scan(
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
			&job.ContentHash,
			&job.LastErrorCode,
			&job.SummaryGenerationKey,
		); scanErr != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return nil, errors.Wrap(scanErr, "scan claimed index jobs")
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		return nil, errors.Wrap(err, "iterate claimed index jobs")
	}
	if closeErr := rows.Close(); closeErr != nil {
		_ = tx.Rollback()
		return nil, errors.Wrap(closeErr, "close claimed index jobs rows")
	}

	if len(jobs) == 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, errors.Wrap(commitErr, "commit empty claim transaction")
		}
		return jobs, nil
	}

	ids := make([]int64, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	inClause, inArgs := buildInClauseInt64(ids, svc.isPostgres, 4)
	updateQuery := rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE system_owner = ? AND id IN (%s)`, svc.isPostgres)
	updateArgs := make([]any, 0, 3+len(inArgs))
	updateArgs = append(updateArgs, "processing", now, "")
	updateArgs = append(updateArgs, inArgs...)
	if _, err = tx.ExecContext(ctx, strings.Replace(updateQuery, "%s", inClause, 1), updateArgs...); err != nil {
		_ = tx.Rollback()
		return nil, errors.Wrap(err, "mark jobs processing")
	}

	if err = tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "commit claim jobs transaction")
	}

	return jobs, nil
}

// processJob dispatches the job by operation type.
func (w *IndexWorker) processJob(ctx context.Context, job FileIndexJob) error {
	svc := w.svc
	var err error
	switch job.Operation {
	case "UPSERT":
		err = svc.processUpsertJob(ctx, job)
	case "DELETE":
		err = svc.processDeleteJob(ctx, job)
	case "SUMMARY_REFRESH":
		// The refresh handler owns its terminal state (done/waiting_auth/retry) so it
		// can move to waiting_auth without consuming a retry (§4.6).
		return w.runSummaryRefresh(ctx, job)
	default:
		return w.markJobFailed(ctx, job, errors.New("unknown job operation"))
	}
	if err != nil {
		return w.handleJobError(ctx, job, err)
	}
	return w.markJobDone(ctx, job)
}

// runSummaryRefresh attempts to upgrade a degraded summary to a validated model
// summary for the same content generation. It never rebuilds or reranks chunks and
// never uses a platform key. Missing credentials move the job to waiting_auth until a
// later authenticated operation reactivates it (§4.6).
func (w *IndexWorker) runSummaryRefresh(ctx context.Context, job FileIndexJob) error {
	svc := w.svc
	file, err := svc.findActiveFile(ctx, job.APIKeyHash, job.Project, job.FilePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.markSummaryRefreshStatus(ctx, job, "done", "")
		}
		return w.handleJobError(ctx, job, errors.Wrap(err, "load file for summary refresh"))
	}
	fileHash := file.ContentHash
	if fileHash == "" {
		fileHash = HashFileContent(file.Content)
	}
	// Superseded by a newer content generation, or already refreshed to ready.
	if job.ContentHash != "" && job.ContentHash != fileHash {
		return w.markSummaryRefreshStatus(ctx, job, "done", "")
	}
	if file.SummaryStatus == string(SummaryStatusReady) && file.SummaryContentHash == fileHash {
		return w.markSummaryRefreshStatus(ctx, job, "done", "")
	}
	if svc.summarizer == nil {
		return w.markSummaryRefreshStatus(ctx, job, "waiting_auth", "summarizer_disabled")
	}

	ref := CredentialReference{APIKeyHash: job.APIKeyHash, Project: job.Project, Path: job.FilePath}
	if job.FileUpdatedAt != nil {
		ref.UpdatedAt = *job.FileUpdatedAt
	}
	apiKey, credErr := svc.loadCredential(ctx, ref)
	if credErr != nil || strings.TrimSpace(apiKey) == "" {
		return w.markSummaryRefreshStatus(ctx, job, "waiting_auth", "credential_unavailable")
	}

	pub := svc.buildSummaryPublication(ctx, apiKey, string(file.Content), fileHash, nil)
	svc.logSummaryGeneration(ctx, "rag_refresh", pub)
	if pub.status != SummaryStatusReady {
		// Still failing: retry with bounded backoff, then mark failed.
		return w.handleSummaryRefreshRetry(ctx, job, pub.errorCode)
	}

	if err := svc.publishSummaryStandalone(ctx, job, pub); err != nil {
		return w.handleJobError(ctx, job, err)
	}
	_ = svc.deleteCredential(ctx, ref)
	svc.summaryRefreshOutcome(ctx, "rag", "done")
	return w.markSummaryRefreshStatus(ctx, job, "done", "")
}

// handleSummaryRefreshRetry requeues a transient refresh failure with bounded backoff
// and marks the job failed once retries are exhausted. The already-published degraded
// summary remains searchable either way.
func (w *IndexWorker) handleSummaryRefreshRetry(ctx context.Context, job FileIndexJob, errorCode string) error {
	svc := w.svc
	if job.RetryCount >= svc.settings.Index.RetryMax {
		svc.summaryRefreshOutcome(ctx, "rag", "failed")
		return w.markSummaryRefreshStatus(ctx, job, "failed", errorCode)
	}
	backoff := svc.settings.Index.RetryBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	next := svc.clock().Add(backoff * time.Duration(job.RetryCount+1))
	svc.summaryRefreshOutcome(ctx, "rag", "retry")
	_, execErr := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, retry_count = ?, available_at = ?, updated_at = ?, last_error_code = ? WHERE id = ? AND system_owner = ?`, svc.isPostgres),
		"pending",
		job.RetryCount+1,
		next,
		svc.clock(),
		errorCode,
		job.ID,
		"",
	)
	return execErr
}

// markSummaryRefreshStatus sets the terminal (or waiting) state for a refresh job.
func (w *IndexWorker) markSummaryRefreshStatus(ctx context.Context, job FileIndexJob, status, errorCode string) error {
	svc := w.svc
	_, err := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ?, last_error_code = ? WHERE id = ? AND system_owner = ?`, svc.isPostgres),
		status,
		svc.clock(),
		errorCode,
		job.ID,
		"",
	)
	return err
}

// handleJobError schedules retries or marks a job as failed.
func (w *IndexWorker) handleJobError(ctx context.Context, job FileIndexJob, err error) error {
	svc := w.svc
	retryMax := svc.settings.Index.RetryMax
	if job.RetryCount >= retryMax {
		return w.markJobFailed(ctx, job, err)
	}
	backoff := svc.settings.Index.RetryBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	next := svc.clock().Add(backoff * time.Duration(job.RetryCount+1))
	_, execErr := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs
		SET status = ?, retry_count = ?, available_at = ?, updated_at = ?
		WHERE id = ? AND system_owner = ?`, svc.isPostgres),
		"pending",
		job.RetryCount+1,
		next,
		svc.clock(),
		job.ID,
		"",
	)
	return execErr
}

// markJobFailed updates the job status to failed.
func (w *IndexWorker) markJobFailed(ctx context.Context, job FileIndexJob, err error) error {
	svc := w.svc
	w.logger.Warn("index job failed", zap.Error(err), zap.Int64("job_id", job.ID))
	_, execErr := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE id = ? AND system_owner = ?`, svc.isPostgres),
		"failed",
		svc.clock(),
		job.ID,
		"",
	)
	return execErr
}

// markJobDone updates the job status to done.
func (w *IndexWorker) markJobDone(ctx context.Context, job FileIndexJob) error {
	svc := w.svc
	_, err := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE id = ? AND system_owner = ?`, svc.isPostgres),
		"done",
		svc.clock(),
		job.ID,
		"",
	)
	return err
}

// processUpsertJob rebuilds search index rows for a file path.
func (s *Service) processUpsertJob(ctx context.Context, job FileIndexJob) error {
	file, err := s.findActiveFile(ctx, job.APIKeyHash, job.Project, job.FilePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.deleteIndexRows(ctx, job.APIKeyHash, job.Project, job.FilePath)
		}
		return errors.Wrap(err, "load file for indexing")
	}
	if job.FileUpdatedAt != nil && file.UpdatedAt.After(*job.FileUpdatedAt) {
		s.LoggerFromContext(ctx).Debug("file index upsert skipped: stale job file_updated_at",
			zap.String("project", job.Project),
			zap.String("file_path", job.FilePath),
			zap.Time("file_updated_at", file.UpdatedAt),
			zap.Time("job_file_updated_at", *job.FileUpdatedAt),
		)
		return nil
	}

	// fileContentHash is the immutable generation identity that binds this file's
	// chunks and summary. A queued job whose expected hash no longer matches the
	// active file is skipped before any model call so rapid successive writes to the
	// same path coalesce to the newest generation (§4.1, §4.2).
	fileContentHash := file.ContentHash
	if fileContentHash == "" {
		fileContentHash = HashFileContent(file.Content)
	}
	if job.ContentHash != "" && fileContentHash != "" && job.ContentHash != fileContentHash {
		s.summaryStaleDiscard(ctx, "rag", job.Project, job.FilePath)
		return nil
	}

	chunks := s.chunker.Split(string(file.Content))
	apiKey := ""
	ref := CredentialReference{APIKeyHash: job.APIKeyHash, Project: job.Project, Path: job.FilePath}
	if job.FileUpdatedAt != nil {
		ref.UpdatedAt = *job.FileUpdatedAt
	}
	if s.contextualizer != nil || s.embedder != nil || s.summarizer != nil {
		loadedAPIKey, loadErr := s.loadCredential(ctx, ref)
		if loadErr != nil {
			s.LoggerFromContext(ctx).Debug("load credential failed before indexing",
				zap.String("project", job.Project),
				zap.String("file_path", job.FilePath),
				zap.Error(loadErr),
			)
		} else {
			apiKey = loadedAPIKey
		}
	}

	// Generate the summary off the transaction path (no model call under a lock).
	pub := s.buildSummaryPublication(ctx, apiKey, string(file.Content), fileContentHash, file)
	s.logSummaryGeneration(ctx, "rag", pub)

	indexContents := s.buildContextualizedChunkInputs(ctx, apiKey, string(file.Content), job.FilePath, chunks)
	plan := s.buildEmbeddingPlan(ctx, job, apiKey, indexContents)
	if err := s.replaceIndexRows(ctx, job, chunks, indexContents, plan.vectors, pub); err != nil {
		return err
	}
	if plan.err != nil {
		s.LoggerFromContext(ctx).Debug("file index upsert degraded to lexical-only",
			zap.String("project", job.Project),
			zap.String("file_path", job.FilePath),
			zap.Int("chunk_count", len(chunks)),
			zap.Error(plan.err),
		)
		return plan.err
	}

	if s.embedder != nil || s.contextualizer != nil || s.summarizer != nil {
		if err := s.deleteCredential(ctx, ref); err != nil {
			return err
		}
	}

	s.LoggerFromContext(ctx).Debug("file index upsert completed",
		zap.String("project", job.Project),
		zap.String("file_path", job.FilePath),
		zap.Int("chunk_count", len(chunks)),
		zap.Int("embedding_count", len(plan.vectors)),
	)

	return nil
}

// buildEmbeddingPlan prepares vectors when semantic indexing is available.
func (s *Service) buildEmbeddingPlan(ctx context.Context, job FileIndexJob, apiKey string, indexContents []string) embeddingPlan {
	if len(indexContents) == 0 {
		return embeddingPlan{}
	}
	if s.embedder == nil {
		return embeddingPlan{}
	}
	if strings.TrimSpace(apiKey) == "" {
		s.LoggerFromContext(ctx).Debug("skip embedding for index upsert: missing credential envelope",
			zap.String("project", job.Project),
			zap.String("file_path", job.FilePath),
		)
		return embeddingPlan{}
	}

	vectors, err := s.embedder.EmbedTexts(ctx, apiKey, indexContents)
	if err != nil {
		return embeddingPlan{err: errors.Wrap(err, "embed chunk contents")}
	}
	if len(vectors) != len(indexContents) {
		return embeddingPlan{err: errors.New("embedding count mismatch")}
	}

	return embeddingPlan{vectors: vectors}
}

// processDeleteJob removes index rows for a file path.
func (s *Service) processDeleteJob(ctx context.Context, job FileIndexJob) error {
	file, err := s.findActiveFile(ctx, job.APIKeyHash, job.Project, job.FilePath)
	if err == nil {
		if job.FileUpdatedAt != nil && file.UpdatedAt.After(*job.FileUpdatedAt) {
			return nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Wrap(err, "check file for delete")
	}
	return s.deleteIndexRows(ctx, job.APIKeyHash, job.Project, job.FilePath)
}

// replaceIndexRows rebuilds all chunk rows and lexical metadata for a file, writes
// embeddings when vectors are provided, and atomically publishes the file summary for
// the same content generation. It first rechecks the active content hash: if a newer
// write arrived while the summary/embeddings were being produced, the stale generation
// is discarded rather than overwriting the newer state (§4.1, §4.2).
func (s *Service) replaceIndexRows(ctx context.Context, job FileIndexJob, chunks []Chunk, indexContents []string, vectors []pgvector.Vector, pub summaryPublication) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin replace index rows transaction")
	}

	curHash, hashErr := s.checkActiveContentHashTx(ctx, tx, job)
	switch {
	case errors.Is(hashErr, sql.ErrNoRows):
		// File was deleted concurrently: drop any leftover index rows and stop.
		if delErr := s.deleteIndexRowsTx(ctx, tx, job.APIKeyHash, job.Project, job.FilePath); delErr != nil {
			_ = tx.Rollback()
			return delErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return errors.Wrap(commitErr, "commit deleted-file index cleanup")
		}
		return nil
	case hashErr != nil:
		_ = tx.Rollback()
		return errors.Wrap(hashErr, "recheck active content hash")
	}
	if curHash != "" && pub.contentHash != "" && curHash != pub.contentHash {
		// A newer generation is already active; discard this stale worker output.
		_ = tx.Rollback()
		s.summaryStaleDiscard(ctx, "rag", job.Project, job.FilePath)
		return nil
	}

	if err := s.deleteIndexRowsTx(ctx, tx, job.APIKeyHash, job.Project, job.FilePath); err != nil {
		_ = tx.Rollback()
		return err
	}

	now := s.clock()
	shouldWriteEmbeddings := len(vectors) == len(chunks)
	shouldUseContextualizedContent := len(indexContents) == len(chunks)

	for i, ch := range chunks {
		chunkID, insertErr := s.insertChunkRowTx(ctx, tx, job, ch, pub.contentHash, now)
		if insertErr != nil {
			_ = tx.Rollback()
			return insertErr
		}
		indexContent := ch.Content
		if shouldUseContextualizedContent && indexContents[i] != "" {
			indexContent = indexContents[i]
		}
		if err := s.insertBM25(ctx, tx, chunkID, indexContent, now); err != nil {
			_ = tx.Rollback()
			return err
		}
		if shouldWriteEmbeddings {
			if err := s.insertEmbedding(ctx, tx, chunkID, vectors[i], now); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}

	// Backfill a legacy-empty content_hash so search can pair chunks and summary.
	if curHash == "" && pub.contentHash != "" {
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET content_hash = ? WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres),
			pub.contentHash, job.APIKeyHash, job.Project, job.FilePath, "",
		); err != nil {
			_ = tx.Rollback()
			return errors.Wrap(err, "backfill content hash")
		}
	}

	// Publish the summary and enqueue a refresh atomically with the chunk generation.
	if err := s.publishSummaryTx(ctx, tx, job, pub, now); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.enqueueSummaryRefreshTx(ctx, tx, job, pub, now); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "commit replace index rows transaction")
	}

	return nil
}

// insertChunkRowTx inserts one chunk row (carrying both the per-chunk content_hash and
// the whole-file file_content_hash) and returns its generated id, hiding the
// Postgres RETURNING vs SQLite LastInsertId dialect split.
func (s *Service) insertChunkRowTx(ctx context.Context, tx *sql.Tx, job FileIndexJob, ch Chunk, fileContentHash string, now time.Time) (int64, error) {
	hash := sha256.Sum256([]byte(ch.Content))
	insertChunkQuery := rebindSQL(`INSERT INTO mcp_file_chunks (apikey_hash, project, file_path, chunk_index, start_byte, end_byte, chunk_content, content_hash, file_content_hash, created_at, updated_at, system_owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.isPostgres)
	args := []any{
		job.APIKeyHash, job.Project, job.FilePath, ch.Index, ch.StartByte, ch.EndByte,
		ch.Content, hex.EncodeToString(hash[:]), fileContentHash, now, now, "",
	}
	if s.isPostgres {
		var chunkID int64
		if err := tx.QueryRowContext(ctx, insertChunkQuery+" RETURNING id", args...).Scan(&chunkID); err != nil {
			return 0, errors.Wrap(err, "insert chunk")
		}
		return chunkID, nil
	}
	result, execErr := tx.ExecContext(ctx, insertChunkQuery, args...)
	if execErr != nil {
		return 0, errors.Wrap(execErr, "insert chunk")
	}
	chunkID, err := result.LastInsertId()
	if err != nil {
		return 0, errors.Wrap(err, "load inserted chunk id")
	}
	return chunkID, nil
}

// insertEmbedding stores a chunk embedding, using pgvector when available.
func (s *Service) insertEmbedding(ctx context.Context, tx *sql.Tx, chunkID int64, vector pgvector.Vector, now time.Time) error {
	if s.isPostgres {
		_, err := tx.ExecContext(ctx,
			rebindSQL(`INSERT INTO mcp_file_chunk_embeddings (chunk_id, embedding, model, created_at, updated_at, system_owner) VALUES (?, ?, ?, ?, ?, ?)`, s.isPostgres),
			chunkID,
			vector,
			s.settings.EmbeddingModel,
			now,
			now,
			"",
		)
		return err
	}

	payload, err := json.Marshal(vector.Slice())
	if err != nil {
		return errors.Wrap(err, "marshal embedding")
	}
	_, err = tx.ExecContext(ctx,
		rebindSQL("INSERT INTO mcp_file_chunk_embeddings (chunk_id, embedding, model, created_at, updated_at, system_owner) VALUES (?, ?, ?, ?, ?, ?)", s.isPostgres),
		chunkID,
		string(payload),
		s.settings.EmbeddingModel,
		now,
		now,
		"",
	)
	return err
}

// insertBM25 stores lexical token metadata for a chunk.
func (s *Service) insertBM25(ctx context.Context, tx *sql.Tx, chunkID int64, content string, now time.Time) error {
	tokens := tokenize(content)
	tokenCounts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		tokenCounts[token]++
	}
	payload, err := json.Marshal(tokenCounts)
	if err != nil {
		return errors.Wrap(err, "marshal tokens")
	}
	_, execErr := tx.ExecContext(ctx,
		rebindSQL(`INSERT INTO mcp_file_chunk_bm25 (chunk_id, tokens, token_count, tokenizer, created_at, updated_at, system_owner) VALUES (?, ?, ?, ?, ?, ?, ?)`, s.isPostgres),
		chunkID,
		payload,
		len(tokens),
		"simple",
		now,
		now,
		"",
	)
	return execErr
}

// deleteIndexRows deletes all index rows for a file path.
func (s *Service) deleteIndexRows(ctx context.Context, apiKeyHash, project, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin delete index rows transaction")
	}
	if err = s.deleteIndexRowsTx(ctx, tx, apiKeyHash, project, path); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "commit delete index rows transaction")
	}
	return nil
}

// deleteIndexRowsTx deletes index rows for a file within a transaction. The index
// worker only operates against user-namespace rows (system_owner=”), so the
// scoped deletes below are explicit about that.
func (s *Service) deleteIndexRowsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) error {
	if _, err := tx.ExecContext(ctx,
		rebindSQL(`DELETE FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ? AND system_owner = ?`, s.isPostgres),
		apiKeyHash,
		project,
		path,
		"",
	); err != nil {
		return errors.Wrap(err, "delete chunks")
	}
	// The orphan cleanups below are keyed by chunk_id (a primary-key reference),
	// so the system_owner predicate on mcp_file_chunks above is sufficient to keep
	// system rows out of scope.
	// system_owner-checked: orphan cleanup; mcp_file_chunks already filtered by system_owner above
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_file_chunk_embeddings WHERE chunk_id NOT IN (SELECT id FROM mcp_file_chunks)"); err != nil {
		return errors.Wrap(err, "cleanup embeddings")
	}
	// system_owner-checked: orphan cleanup; mcp_file_chunks already filtered by system_owner above
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_file_chunk_bm25 WHERE chunk_id NOT IN (SELECT id FROM mcp_file_chunks)"); err != nil {
		return errors.Wrap(err, "cleanup bm25")
	}
	return nil
}

// loadCredential decrypts the cached credential envelope for a job.
func (s *Service) loadCredential(ctx context.Context, ref CredentialReference) (string, error) {
	if s.credential == nil || s.credStore == nil {
		return "", errors.New("credential store not configured")
	}
	key := ref.CacheKey(s.settings.Security.CredentialCachePrefix)
	payload, err := s.credStore.Load(ctx, key)
	if err != nil {
		return "", errors.Wrap(err, "load credential envelope")
	}
	apiKey, err := s.credential.DecryptCredential(ctx, payload, ref.AAD())
	if err != nil {
		return "", errors.Wrap(err, "decrypt credential")
	}
	return apiKey, nil
}

// deleteCredential removes the cached credential envelope after use.
func (s *Service) deleteCredential(ctx context.Context, ref CredentialReference) error {
	if s.credStore == nil {
		return nil
	}
	key := ref.CacheKey(s.settings.Security.CredentialCachePrefix)
	if err := s.credStore.Delete(ctx, key); err != nil {
		s.LoggerFromContext(ctx).Warn("delete credential envelope", zap.Error(err), zap.String("key", key))
	}
	return nil
}

// buildInClauseInt64 returns placeholders and args for int64 IN clauses.
func buildInClauseInt64(values []int64, isPostgres bool, startIndex int) (string, []any) {
	placeholders := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for i, value := range values {
		if isPostgres {
			placeholders = append(placeholders, "$"+strconvItoa(startIndex+i))
		} else {
			placeholders = append(placeholders, "?")
		}
		args = append(args, value)
	}
	return strings.Join(placeholders, ","), args
}
