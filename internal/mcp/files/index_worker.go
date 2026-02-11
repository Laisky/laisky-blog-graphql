package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

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

	var jobs []FileIndexJob
	err := svc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := "SELECT * FROM mcp_file_index_jobs WHERE status = ? AND available_at <= ? ORDER BY id ASC LIMIT ?"
		args := []any{"pending", now, batch}
		if isPostgresDialect(tx) {
			query += " FOR UPDATE SKIP LOCKED"
		}
		if err := tx.Raw(query, args...).Scan(&jobs).Error; err != nil {
			return errors.Wrap(err, "claim index jobs")
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(jobs))
		for _, job := range jobs {
			ids = append(ids, job.ID)
		}
		if err := tx.Model(&FileIndexJob{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":     "processing",
				"updated_at": now,
			}).Error; err != nil {
			return errors.Wrap(err, "mark jobs processing")
		}
		return nil
	})
	if err != nil {
		return nil, err
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
	return svc.db.WithContext(ctx).Model(&FileIndexJob{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":       "pending",
			"retry_count":  job.RetryCount + 1,
			"available_at": next,
			"updated_at":   svc.clock(),
		}).Error
}

// markJobFailed updates the job status to failed.
func (w *IndexWorker) markJobFailed(ctx context.Context, job FileIndexJob, err error) error {
	svc := w.svc
	w.logger.Warn("index job failed", zap.Error(err), zap.Int64("job_id", job.ID))
	return svc.db.WithContext(ctx).Model(&FileIndexJob{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":     "failed",
			"updated_at": svc.clock(),
		}).Error
}

// markJobDone updates the job status to done.
func (w *IndexWorker) markJobDone(ctx context.Context, job FileIndexJob) error {
	svc := w.svc
	return svc.db.WithContext(ctx).Model(&FileIndexJob{}).
		Where("id = ?", job.ID).
		Updates(map[string]any{
			"status":     "done",
			"updated_at": svc.clock(),
		}).Error
}

// processUpsertJob rebuilds search index rows for a file path.
func (s *Service) processUpsertJob(ctx context.Context, job FileIndexJob) error {
	if s.embedder == nil {
		return errors.New("embedder not configured")
	}
	file, err := s.findActiveFile(ctx, job.APIKeyHash, job.Project, job.FilePath)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.deleteIndexRows(ctx, job.APIKeyHash, job.Project, job.FilePath)
		}
		return errors.Wrap(err, "load file for indexing")
	}
	if job.FileUpdatedAt != nil && file.UpdatedAt.After(*job.FileUpdatedAt) {
		return nil
	}

	ref := CredentialReference{APIKeyHash: job.APIKeyHash, Project: job.Project, Path: job.FilePath}
	if job.FileUpdatedAt != nil {
		ref.UpdatedAt = *job.FileUpdatedAt
	}
	apiKey, err := s.loadCredential(ctx, ref)
	if err != nil {
		return err
	}

	chunks := s.chunker.Split(string(file.Content))
	if err := s.replaceIndexRows(ctx, job, chunks, apiKey); err != nil {
		return err
	}
	return s.deleteCredential(ctx, ref)
}

// processDeleteJob removes index rows for a file path.
func (s *Service) processDeleteJob(ctx context.Context, job FileIndexJob) error {
	file, err := s.findActiveFile(ctx, job.APIKeyHash, job.Project, job.FilePath)
	if err == nil {
		if job.FileUpdatedAt != nil && file.UpdatedAt.After(*job.FileUpdatedAt) {
			return nil
		}
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.Wrap(err, "check file for delete")
	}
	return s.deleteIndexRows(ctx, job.APIKeyHash, job.Project, job.FilePath)
}

// replaceIndexRows rebuilds all chunk, embedding, and bm25 rows for a file.
func (s *Service) replaceIndexRows(ctx context.Context, job FileIndexJob, chunks []Chunk, apiKey string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.deleteIndexRowsTx(ctx, tx, job.APIKeyHash, job.Project, job.FilePath); err != nil {
			return err
		}

		now := s.clock()
		if len(chunks) == 0 {
			return nil
		}
		contents := make([]string, 0, len(chunks))
		for _, ch := range chunks {
			contents = append(contents, ch.Content)
		}
		vectors, err := s.embedder.EmbedTexts(ctx, apiKey, contents)
		if err != nil {
			return errors.Wrap(err, "embed chunk contents")
		}
		if len(vectors) != len(chunks) {
			return errors.New("embedding count mismatch")
		}

		for i, ch := range chunks {
			hash := sha256.Sum256([]byte(ch.Content))
			chunk := FileChunk{
				APIKeyHash:  job.APIKeyHash,
				Project:     job.Project,
				FilePath:    job.FilePath,
				ChunkIndex:  ch.Index,
				StartByte:   ch.StartByte,
				EndByte:     ch.EndByte,
				Content:     ch.Content,
				ContentHash: hex.EncodeToString(hash[:]),
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := tx.WithContext(ctx).Create(&chunk).Error; err != nil {
				return errors.Wrap(err, "insert chunk")
			}
			if err := s.insertEmbedding(ctx, tx, chunk.ID, vectors[i], now); err != nil {
				return err
			}
			if err := s.insertBM25(ctx, tx, chunk.ID, ch.Content, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// insertEmbedding stores a chunk embedding, using pgvector when available.
func (s *Service) insertEmbedding(ctx context.Context, tx *gorm.DB, chunkID int64, vector pgvector.Vector, now time.Time) error {
	if isPostgresDialect(tx) {
		row := FileChunkEmbedding{
			ChunkID:   chunkID,
			Embedding: vector,
			Model:     s.settings.EmbeddingModel,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return tx.WithContext(ctx).Create(&row).Error
	}

	payload, err := json.Marshal(vector.Slice())
	if err != nil {
		return errors.Wrap(err, "marshal embedding")
	}
	return tx.WithContext(ctx).Exec(
		"INSERT INTO mcp_file_chunk_embeddings (chunk_id, embedding, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		chunkID,
		string(payload),
		s.settings.EmbeddingModel,
		now,
		now,
	).Error
}

// insertBM25 stores lexical token metadata for a chunk.
func (s *Service) insertBM25(ctx context.Context, tx *gorm.DB, chunkID int64, content string, now time.Time) error {
	tokens := tokenize(content)
	tokenCounts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		tokenCounts[token]++
	}
	payload, err := json.Marshal(tokenCounts)
	if err != nil {
		return errors.Wrap(err, "marshal tokens")
	}
	row := FileChunkBM25{
		ChunkID:    chunkID,
		Tokens:     payload,
		TokenCount: len(tokens),
		Tokenizer:  "simple",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return tx.WithContext(ctx).Create(&row).Error
}

// deleteIndexRows deletes all index rows for a file path.
func (s *Service) deleteIndexRows(ctx context.Context, apiKeyHash, project, path string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.deleteIndexRowsTx(ctx, tx, apiKeyHash, project, path)
	})
}

// deleteIndexRowsTx deletes index rows for a file within a transaction.
func (s *Service) deleteIndexRowsTx(ctx context.Context, tx *gorm.DB, apiKeyHash, project, path string) error {
	if err := tx.WithContext(ctx).Where("apikey_hash = ? AND project = ? AND file_path = ?", apiKeyHash, project, path).Delete(&FileChunk{}).Error; err != nil {
		return errors.Wrap(err, "delete chunks")
	}
	if err := tx.WithContext(ctx).Where("chunk_id NOT IN (SELECT id FROM mcp_file_chunks)").Delete(&FileChunkEmbedding{}).Error; err != nil {
		return errors.Wrap(err, "cleanup embeddings")
	}
	if err := tx.WithContext(ctx).Where("chunk_id NOT IN (SELECT id FROM mcp_file_chunks)").Delete(&FileChunkBM25{}).Error; err != nil {
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
