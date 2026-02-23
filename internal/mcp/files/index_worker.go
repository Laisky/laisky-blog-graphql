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

// Start runs the worker loop until the context is cancelled.
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

	query := "SELECT id, apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at FROM mcp_file_index_jobs WHERE status = ? AND available_at <= ? ORDER BY id ASC LIMIT ?"
	args := []any{"pending", now, batch}
	if svc.isPostgres {
		query += " FOR UPDATE SKIP LOCKED"
	}

	rows, err := tx.QueryContext(ctx, rebindSQL(query, svc.isPostgres), args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, errors.Wrap(err, "claim index jobs")
	}
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
		); scanErr != nil {
			rows.Close()
			_ = tx.Rollback()
			return nil, errors.Wrap(scanErr, "scan claimed index jobs")
		}
		jobs = append(jobs, job)
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
	inClause, inArgs := buildInClauseInt64(ids, svc.isPostgres, 3)
	updateQuery := rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE id IN (%s)`, svc.isPostgres)
	updateArgs := []any{"processing", now}
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
	default:
		return w.markJobFailed(ctx, job, errors.New("unknown job operation"))
	}
	if err != nil {
		return w.handleJobError(ctx, job, err)
	}
	return w.markJobDone(ctx, job)
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
		WHERE id = ?`, svc.isPostgres),
		"pending",
		job.RetryCount+1,
		next,
		svc.clock(),
		job.ID,
	)
	return execErr
}

// markJobFailed updates the job status to failed.
func (w *IndexWorker) markJobFailed(ctx context.Context, job FileIndexJob, err error) error {
	svc := w.svc
	w.logger.Warn("index job failed", zap.Error(err), zap.Int64("job_id", job.ID))
	_, execErr := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE id = ?`, svc.isPostgres),
		"failed",
		svc.clock(),
		job.ID,
	)
	return execErr
}

// markJobDone updates the job status to done.
func (w *IndexWorker) markJobDone(ctx context.Context, job FileIndexJob) error {
	svc := w.svc
	_, err := svc.db.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, updated_at = ? WHERE id = ?`, svc.isPostgres),
		"done",
		svc.clock(),
		job.ID,
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

	chunks := s.chunker.Split(string(file.Content))
	apiKey := ""
	ref := CredentialReference{APIKeyHash: job.APIKeyHash, Project: job.Project, Path: job.FilePath}
	if job.FileUpdatedAt != nil {
		ref.UpdatedAt = *job.FileUpdatedAt
	}
	if s.contextualizer != nil || s.embedder != nil {
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

	indexContents := s.buildContextualizedChunkInputs(ctx, apiKey, string(file.Content), job.FilePath, chunks)
	plan := s.buildEmbeddingPlan(ctx, job, indexContents)
	if err := s.replaceIndexRows(ctx, job, chunks, indexContents, plan.vectors); err != nil {
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

	if s.embedder != nil || s.contextualizer != nil {
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
func (s *Service) buildEmbeddingPlan(ctx context.Context, job FileIndexJob, indexContents []string) embeddingPlan {
	if len(indexContents) == 0 {
		return embeddingPlan{}
	}
	if s.embedder == nil {
		return embeddingPlan{}
	}

	ref := CredentialReference{APIKeyHash: job.APIKeyHash, Project: job.Project, Path: job.FilePath}
	if job.FileUpdatedAt != nil {
		ref.UpdatedAt = *job.FileUpdatedAt
	}

	apiKey, err := s.loadCredential(ctx, ref)
	if err != nil {
		return embeddingPlan{err: err}
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

// replaceIndexRows rebuilds all chunk rows and lexical metadata for a file,
// and writes embeddings when vectors are provided.
func (s *Service) replaceIndexRows(ctx context.Context, job FileIndexJob, chunks []Chunk, indexContents []string, vectors []pgvector.Vector) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin replace index rows transaction")
	}

	if err := s.deleteIndexRowsTx(ctx, tx, job.APIKeyHash, job.Project, job.FilePath); err != nil {
		_ = tx.Rollback()
		return err
	}

	now := s.clock()
	if len(chunks) == 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return errors.Wrap(commitErr, "commit empty replace index rows")
		}
		return nil
	}
	shouldWriteEmbeddings := len(vectors) == len(chunks)
	shouldUseContextualizedContent := len(indexContents) == len(chunks)

	for i, ch := range chunks {
		hash := sha256.Sum256([]byte(ch.Content))
		insertChunkQuery := rebindSQL(`INSERT INTO mcp_file_chunks (apikey_hash, project, file_path, chunk_index, start_byte, end_byte, chunk_content, content_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.isPostgres)
		var chunkID int64
		if s.isPostgres {
			if err = tx.QueryRowContext(ctx, insertChunkQuery+" RETURNING id",
				job.APIKeyHash,
				job.Project,
				job.FilePath,
				ch.Index,
				ch.StartByte,
				ch.EndByte,
				ch.Content,
				hex.EncodeToString(hash[:]),
				now,
				now,
			).Scan(&chunkID); err != nil {
				_ = tx.Rollback()
				return errors.Wrap(err, "insert chunk")
			}
		} else {
			result, execErr := tx.ExecContext(ctx, insertChunkQuery,
				job.APIKeyHash,
				job.Project,
				job.FilePath,
				ch.Index,
				ch.StartByte,
				ch.EndByte,
				ch.Content,
				hex.EncodeToString(hash[:]),
				now,
				now,
			)
			if execErr != nil {
				_ = tx.Rollback()
				return errors.Wrap(execErr, "insert chunk")
			}
			chunkID, err = result.LastInsertId()
			if err != nil {
				_ = tx.Rollback()
				return errors.Wrap(err, "load inserted chunk id")
			}
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

	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "commit replace index rows transaction")
	}

	return nil
}

// insertEmbedding stores a chunk embedding, using pgvector when available.
func (s *Service) insertEmbedding(ctx context.Context, tx *sql.Tx, chunkID int64, vector pgvector.Vector, now time.Time) error {
	if s.isPostgres {
		_, err := tx.ExecContext(ctx,
			rebindSQL(`INSERT INTO mcp_file_chunk_embeddings (chunk_id, embedding, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, s.isPostgres),
			chunkID,
			vector,
			s.settings.EmbeddingModel,
			now,
			now,
		)
		return err
	}

	payload, err := json.Marshal(vector.Slice())
	if err != nil {
		return errors.Wrap(err, "marshal embedding")
	}
	_, err = tx.ExecContext(ctx,
		rebindSQL("INSERT INTO mcp_file_chunk_embeddings (chunk_id, embedding, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", s.isPostgres),
		chunkID,
		string(payload),
		s.settings.EmbeddingModel,
		now,
		now,
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
		rebindSQL(`INSERT INTO mcp_file_chunk_bm25 (chunk_id, tokens, token_count, tokenizer, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, s.isPostgres),
		chunkID,
		payload,
		len(tokens),
		"simple",
		now,
		now,
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

// deleteIndexRowsTx deletes index rows for a file within a transaction.
func (s *Service) deleteIndexRowsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) error {
	if _, err := tx.ExecContext(ctx,
		rebindSQL(`DELETE FROM mcp_file_chunks WHERE apikey_hash = ? AND project = ? AND file_path = ?`, s.isPostgres),
		apiKeyHash,
		project,
		path,
	); err != nil {
		return errors.Wrap(err, "delete chunks")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_file_chunk_embeddings WHERE chunk_id NOT IN (SELECT id FROM mcp_file_chunks)"); err != nil {
		return errors.Wrap(err, "cleanup embeddings")
	}
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
