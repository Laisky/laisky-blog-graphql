package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// summaryPublication is the decision + payload for one file summary generation,
// produced off the transaction path (no model call under a lock) and then published
// atomically with the corresponding chunk generation (§4.1, §4.2).
type summaryPublication struct {
	contentHash    string
	publish        bool // whether summary_* columns should be written
	text           string
	wordCount      int
	source         SummarySource
	status         SummaryStatus
	model          string
	promptVersion  string
	generationKey  string
	errorCode      string
	enqueueRefresh bool
	usedModelCall  bool
}

// buildSummaryPublication decides whether to reuse, generate, or fall back to a
// deterministic summary for the given content generation. It performs the model call
// (if any) here, outside any transaction. A ready model summary is returned when the
// output validates; otherwise a bounded deterministic fallback marked degraded is
// returned together with a request to enqueue a SUMMARY_REFRESH job (§4.4, §4.6).
func (s *Service) buildSummaryPublication(ctx context.Context, apiKey, content, fileContentHash string, existing *File) summaryPublication {
	cfg := s.settings.Index.FileSummary
	maxWords, maxBytes := ClampSummaryLimits(cfg.MaxWords, cfg.MaxBytes)
	genKey := summaryGenerationKey(fileContentHash, cfg.Model, cfg.PromptVersion, maxWords, maxBytes)

	// Reuse an existing ready summary for the same content generation. This makes a
	// content-preserving rename and a coalesced re-index free of model calls (§4.3, G5).
	if existing != nil && existing.SummaryContentHash == fileContentHash && existing.SummaryStatus == string(SummaryStatusReady) {
		return summaryPublication{contentHash: fileContentHash, publish: false}
	}

	pub := summaryPublication{
		contentHash:   fileContentHash,
		publish:       true,
		promptVersion: cfg.PromptVersion,
		generationKey: genKey,
	}

	switch {
	case s.summarizer != nil && strings.TrimSpace(apiKey) != "":
		raw, err := s.summarizer.GenerateFileSummary(ctx, apiKey, content)
		pub.usedModelCall = true
		if err == nil {
			if text, wc, ok := NormalizeSummary(raw, maxWords, maxBytes); ok {
				pub.text = text
				pub.wordCount = wc
				pub.source = SummarySourceModel
				pub.status = SummaryStatusReady
				pub.model = cfg.Model
				return pub
			}
			pub.errorCode = "invalid_summary_output"
		} else {
			pub.errorCode = classifySummaryError(err)
		}
	case s.summarizer == nil:
		pub.errorCode = "summarizer_disabled"
	default:
		pub.errorCode = "credential_unavailable"
	}

	// Degraded deterministic fallback: always bounded and grounded, never rejected.
	pub.text = DeterministicFileSummaryFallback(content, maxWords, maxBytes)
	pub.wordCount = SummaryWordCount(pub.text)
	pub.source = SummarySourceDeterministicFallback
	pub.status = SummaryStatusDegraded
	pub.model = ""
	pub.enqueueRefresh = true
	return pub
}

// summaryGenerationKey is SHA-256 over the content hash, model, prompt version, and
// effective limits. A model, prompt, or limit change alters the key so a refresh can
// be scheduled even when the file bytes are unchanged (§5.1).
func summaryGenerationKey(contentHash, model, promptVersion string, maxWords, maxBytes int) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d", contentHash, model, promptVersion, maxWords, maxBytes)
	return hex.EncodeToString(h.Sum(nil))
}

// classifySummaryError maps a generation error to a safe machine code that never
// echoes source content or provider bodies (§7.2).
func classifySummaryError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errSummaryBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "provider_error"
	}
}

// checkActiveContentHashTx returns the current stored content_hash for the active
// user row, acquiring a row lock on Postgres. It reports sql.ErrNoRows when the row
// is gone (deleted concurrently).
func (s *Service) checkActiveContentHashTx(ctx context.Context, tx *sql.Tx, job FileIndexJob) (string, error) {
	sel := `SELECT content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE LIMIT 1`
	if s.isPostgres {
		sel += " FOR UPDATE"
	}
	var curHash string
	err := tx.QueryRowContext(ctx, rebindSQL(sel, s.isPostgres), job.APIKeyHash, job.Project, job.FilePath, "").Scan(&curHash)
	if err != nil {
		return "", err
	}
	return curHash, nil
}

// publishSummaryTx writes the validated summary metadata onto the active user row.
// It is a no-op when the publication only reuses an existing summary.
func (s *Service) publishSummaryTx(ctx context.Context, tx *sql.Tx, job FileIndexJob, pub summaryPublication, now time.Time) error {
	if !pub.publish {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_files SET
			file_summary = ?, summary_content_hash = ?, summary_word_count = ?,
			summary_source = ?, summary_model = ?, summary_prompt_version = ?,
			summary_generation_key = ?, summary_status = ?, summary_updated_at = ?,
			summary_error_code = ?
		WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres),
		pub.text,
		pub.contentHash,
		pub.wordCount,
		string(pub.source),
		pub.model,
		pub.promptVersion,
		pub.generationKey,
		string(pub.status),
		now,
		pub.errorCode,
		job.APIKeyHash,
		job.Project,
		job.FilePath,
		"",
	)
	if err != nil {
		return errors.Wrap(err, "publish file summary")
	}
	return nil
}

// PluginSummaryInput carries a plugin-produced summary for a user file row.
type PluginSummaryInput struct {
	// ExpectedContentHash gates the write: the summary is published only when the
	// active row's content_hash still equals this value, so an older writer cannot
	// overwrite a newer generation (§4.5, P06).
	ExpectedContentHash string
	Summary             string
	WordCount           int
	Source              SummarySource
	Status              SummaryStatus
	Model               string
	PromptVersion       string
	GenerationKey       string
	ErrorCode           string
}

// PublishPluginSummary sets file-summary catalog metadata on an active user row from a
// non-RAG plugin (e.g. PageIndex), without routing through the RAG index worker. The
// write runs under the project mutation lock and is conditional on ExpectedContentHash
// so stale writers are rejected. It returns whether the summary was published (§4.5).
func (s *Service) PublishPluginSummary(ctx context.Context, auth AuthContext, project, path string, in PluginSummaryInput) (bool, error) {
	if err := s.validateAuth(auth); err != nil {
		return false, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return false, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return false, errors.WithStack(err)
	}
	published := false
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		var curHash string
		sel := `SELECT content_hash FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE LIMIT 1`
		if s.isPostgres {
			sel += " FOR UPDATE"
		}
		scanErr := tx.QueryRowContext(ctx, rebindSQL(sel, s.isPostgres), auth.APIKeyHash, project, path, "").Scan(&curHash)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil // row gone; nothing to publish
		}
		if scanErr != nil {
			return errors.Wrap(scanErr, "load content hash for plugin summary")
		}
		if in.ExpectedContentHash != "" && curHash != "" && curHash != in.ExpectedContentHash {
			return nil // stale writer
		}
		effHash := in.ExpectedContentHash
		if effHash == "" {
			effHash = curHash
		}
		now := s.clock()
		if _, execErr := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET
				file_summary = ?, summary_content_hash = ?, summary_word_count = ?,
				summary_source = ?, summary_model = ?, summary_prompt_version = ?,
				summary_generation_key = ?, summary_status = ?, summary_updated_at = ?,
				summary_error_code = ?, content_hash = ?
			WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE`, s.isPostgres),
			in.Summary, effHash, in.WordCount, string(in.Source), in.Model, in.PromptVersion, in.GenerationKey, string(in.Status), now, in.ErrorCode, effHash,
			auth.APIKeyHash, project, path, "",
		); execErr != nil {
			return errors.Wrap(execErr, "publish plugin summary")
		}
		published = true
		return nil
	})
	if err != nil {
		return false, errors.WithStack(err)
	}
	return published, nil
}

// publishSummaryStandalone updates only the summary metadata on the active user row,
// guarded by the content hash so a refresh cannot overwrite a newer generation. It
// never touches chunk rows (§4.6).
func (s *Service) publishSummaryStandalone(ctx context.Context, job FileIndexJob, pub summaryPublication) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin summary refresh transaction")
	}
	curHash, hashErr := s.checkActiveContentHashTx(ctx, tx, job)
	if hashErr != nil {
		_ = tx.Rollback()
		if errors.Is(hashErr, sql.ErrNoRows) {
			return nil
		}
		return errors.Wrap(hashErr, "recheck active content hash for refresh")
	}
	if curHash != "" && pub.contentHash != "" && curHash != pub.contentHash {
		_ = tx.Rollback()
		return nil
	}
	if err := s.publishSummaryTx(ctx, tx, job, pub, s.clock()); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit summary refresh transaction")
	}
	return nil
}

// enqueueSummaryRefreshTx enqueues a deduplicated SUMMARY_REFRESH job for a degraded
// summary. Dedup is keyed by (owner, project, path, content_hash, generation_key)
// across non-terminal states so repeated degraded publications coalesce (§4.6).
func (s *Service) enqueueSummaryRefreshTx(ctx context.Context, tx *sql.Tx, job FileIndexJob, pub summaryPublication, now time.Time) error {
	if !pub.enqueueRefresh {
		return nil
	}
	var cnt int
	if err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT COUNT(1) FROM mcp_file_index_jobs
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND content_hash = ? AND summary_generation_key = ? AND system_owner = ? AND status IN (?, ?, ?)`, s.isPostgres),
		job.APIKeyHash, job.Project, job.FilePath, "SUMMARY_REFRESH", pub.contentHash, pub.generationKey, "",
		"pending", "processing", "waiting_auth",
	).Scan(&cnt); err != nil {
		return errors.Wrap(err, "check summary refresh dedup")
	}
	if cnt > 0 {
		return nil
	}
	backoff := s.settings.Index.RetryBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	return s.insertIndexJobTx(ctx, tx, FileIndexJob{
		APIKeyHash:           job.APIKeyHash,
		Project:              job.Project,
		FilePath:             job.FilePath,
		Operation:            "SUMMARY_REFRESH",
		FileUpdatedAt:        job.FileUpdatedAt,
		// "pending" here is the index-job lifecycle status (see index_worker.go), not
		// the SummaryStatus enum; the two domains share the word by coincidence.
		Status:               "pending", //nolint:goconst // job status, not SummaryStatusPending
		RetryCount:           0,
		AvailableAt:          now.Add(backoff),
		CreatedAt:            now,
		UpdatedAt:            now,
		ContentHash:          pub.contentHash,
		LastErrorCode:        pub.errorCode,
		SummaryGenerationKey: pub.generationKey,
	})
}
