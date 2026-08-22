package files

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// SummaryBackfillOptions selects one bounded, resumable RAG summary backfill
// page. AfterPath is an exclusive lexicographic cursor returned by the previous
// page, so an interrupted operator run can resume without replaying the prefix.
type SummaryBackfillOptions struct {
	APIKeyHash string
	Project    string
	AfterPath  string
	BatchSize  int
}

// SummaryBackfillResult reports the page boundary and durable work scheduled.
type SummaryBackfillResult struct {
	Processed int
	Enqueued  int
	NextPath  string
	Remaining int
	Done      bool
}

// BackfillRAGSummaries computes whole-file hashes and deterministic degraded
// summaries for legacy RAG rows, then schedules normal UPSERT processing to
// replay the current chunk plan. It is bounded, idempotent, and safe to resume
// with NextPath. PageIndex-owned rows are excluded because their trees require
// the plugin's own reindex lifecycle.
func (s *Service) BackfillRAGSummaries(ctx context.Context, opts SummaryBackfillOptions) (SummaryBackfillResult, error) {
	if opts.APIKeyHash == "" {
		return SummaryBackfillResult{}, errors.WithStack(NewError(ErrCodePermissionDenied, "missing api key hash", false))
	}
	if err := ValidateProject(opts.Project); err != nil {
		return SummaryBackfillResult{}, errors.WithStack(err)
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	if batchSize > 1000 {
		batchSize = 1000
	}
	maxWords, maxBytes := ClampSummaryLimits(s.settings.Index.FileSummary.MaxWords, s.settings.Index.FileSummary.MaxBytes)
	genPrompt := s.settings.Index.FileSummary.PromptVersion
	result := SummaryBackfillResult{}
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, opts.APIKeyHash, opts.Project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		query := `SELECT id, path, content, updated_at, content_hash, file_summary, summary_content_hash, summary_status
			FROM mcp_files
			WHERE apikey_hash = ? AND project = ? AND system_owner = '' AND deleted = FALSE AND skip_rag_index = FALSE AND path > ?
			AND (content_hash = '' OR summary_content_hash <> content_hash OR summary_status NOT IN (?, ?))
			ORDER BY path ASC LIMIT ?`
		rows, err := tx.QueryContext(ctx, rebindSQL(query, s.isPostgres), opts.APIKeyHash, opts.Project, opts.AfterPath, string(SummaryStatusReady), string(SummaryStatusDegraded), batchSize)
		if err != nil {
			return errors.Wrap(err, "query summary backfill page")
		}
		defer func() { _ = rows.Close() }()
		type row struct {
			id                 uint64
			path               string
			content            []byte
			updatedAt          time.Time
			contentHash        string
			fileSummary        string
			summaryContentHash string
			summaryStatus      string
		}
		rowsToProcess := make([]row, 0, batchSize)
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.path, &item.content, &item.updatedAt, &item.contentHash, &item.fileSummary, &item.summaryContentHash, &item.summaryStatus); err != nil {
				return errors.Wrap(err, "scan summary backfill row")
			}
			rowsToProcess = append(rowsToProcess, item)
		}
		if err := rows.Err(); err != nil {
			return errors.Wrap(err, "iterate summary backfill rows")
		}
		if err := rows.Close(); err != nil {
			return errors.Wrap(err, "close summary backfill rows")
		}
		result.Processed = len(rowsToProcess)
		if len(rowsToProcess) < batchSize {
			result.Done = true
		}
		if len(rowsToProcess) == 0 {
			result.Remaining = 0
		} else {
			if err := tx.QueryRowContext(ctx,
				rebindSQL(`SELECT COUNT(1) FROM mcp_files
					WHERE apikey_hash = ? AND project = ? AND system_owner = '' AND deleted = FALSE AND skip_rag_index = FALSE AND path > ?
					AND (content_hash = '' OR summary_content_hash <> content_hash OR summary_status NOT IN (?, ?))`, s.isPostgres),
				opts.APIKeyHash,
				opts.Project,
				rowsToProcess[len(rowsToProcess)-1].path,
				string(SummaryStatusReady),
				string(SummaryStatusDegraded),
			).Scan(&result.Remaining); err != nil {
				return errors.Wrap(err, "count remaining summary backfill rows")
			}
		}
		for _, item := range rowsToProcess {
			if err := ctx.Err(); err != nil {
				return errors.WithStack(err)
			}
			contentHash := item.contentHash
			if contentHash == "" {
				contentHash = HashFileContent(item.content)
			}
			fallback := DeterministicFileSummaryFallback(string(item.content), maxWords, maxBytes)
			// The provisional key deliberately differs from the active generation key;
			// the normal UPSERT worker must get one chance to replace this deterministic
			// backfill value with a model-backed summary when credentials are available.
			generationKey := "backfill_pending"
			now := s.clock()
			if _, err := tx.ExecContext(ctx,
				rebindSQL(`UPDATE mcp_files SET content_hash = ?, file_summary = ?, summary_content_hash = ?, summary_word_count = ?,
					summary_source = ?, summary_model = '', summary_prompt_version = ?, summary_generation_key = ?,
					summary_status = ?, summary_updated_at = ?, summary_error_code = ?
					WHERE id = ? AND apikey_hash = ? AND project = ? AND system_owner = '' AND deleted = FALSE`, s.isPostgres),
				contentHash,
				fallback,
				contentHash,
				SummaryWordCount(fallback),
				string(SummarySourceDeterministicFallback),
				genPrompt,
				generationKey,
				string(SummaryStatusDegraded),
				now,
				"backfill_pending",
				item.id,
				opts.APIKeyHash,
				opts.Project,
			); err != nil {
				return errors.Wrap(err, "publish backfill summary")
			}

			var pending int
			if err := tx.QueryRowContext(ctx,
				rebindSQL(`SELECT COUNT(1) FROM mcp_file_index_jobs
					WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND content_hash = ? AND system_owner = '' AND status IN (?, ?, ?)`, s.isPostgres),
				opts.APIKeyHash,
				opts.Project,
				item.path,
				"UPSERT",
				contentHash,
				"pending",
				"processing",
				"waiting_auth",
			).Scan(&pending); err != nil {
				return errors.Wrap(err, "check backfill index job")
			}
			if pending == 0 {
				if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
					APIKeyHash:    opts.APIKeyHash,
					Project:       opts.Project,
					FilePath:      item.path,
					Operation:     "UPSERT",
					FileUpdatedAt: &item.updatedAt,
					Status:        "pending",
					AvailableAt:   now,
					CreatedAt:     now,
					UpdatedAt:     now,
					ContentHash:   contentHash,
				}); err != nil {
					return errors.Wrap(err, "enqueue backfill index job")
				}
				result.Enqueued++
			}
			result.NextPath = item.path
		}
		return nil
	})
	if err != nil {
		return SummaryBackfillResult{}, errors.WithStack(err)
	}
	s.summaryBackfillProgress(ctx, opts.Project, result)
	return result, nil
}
