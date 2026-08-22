package files

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// reactivateSummaryRefreshJobs moves waiting refresh jobs back to the runnable
// state after a fresh credential envelope is stored. The updated timestamp is
// replaced as well, so the worker loads the credential that was just written.
// Only jobs whose content and generation still match the active file are
// reactivated; superseded jobs remain terminally stale.
func (s *Service) reactivateSummaryRefreshJobs(ctx context.Context, q sqlQueryExecutor, ref CredentialReference) error {
	var contentHash, generationKey string
	err := q.QueryRowContext(ctx,
		rebindSQL(`SELECT content_hash, summary_generation_key FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = '' AND deleted = FALSE LIMIT 1`, s.isPostgres),
		ref.APIKeyHash,
		ref.Project,
		ref.Path,
	).Scan(&contentHash, &generationKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "load active file generation")
	}
	now := s.clock()
	_, err = q.ExecContext(ctx,
		rebindSQL(`UPDATE mcp_file_index_jobs SET status = ?, available_at = ?, updated_at = ?, file_updated_at = ?, last_error_code = ?
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND system_owner = ? AND status = ? AND content_hash = ? AND summary_generation_key = ?`, s.isPostgres),
		"pending",
		now,
		now,
		ref.UpdatedAt,
		"",
		ref.APIKeyHash,
		ref.Project,
		ref.Path,
		"SUMMARY_REFRESH",
		"",
		"waiting_auth",
		contentHash,
		generationKey,
	)
	return err
}

type sqlQueryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// storeCredentialEnvelopeTx stores a credential and reactivates waiting refresh
// jobs on the caller's transaction. It is used by write and rename paths, which
// already hold the project lock and must not open a second database transaction.
func (s *Service) storeCredentialEnvelopeTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path string, updatedAt time.Time) error {
	if !s.settings.Search.Enabled {
		return nil
	}
	if s.credential == nil || s.credStore == nil {
		return NewError(ErrCodeSearchBackend, "credential store not configured", false)
	}
	ref := CredentialReference{
		APIKeyHash: auth.APIKeyHash,
		Project:    project,
		Path:       path,
		UpdatedAt:  updatedAt,
	}
	payload, err := s.credential.EncryptCredential(ctx, auth.APIKey, ref.AAD())
	if err != nil {
		return errors.Wrap(err, "encrypt credential")
	}
	key := ref.CacheKey(s.settings.Security.CredentialCachePrefix)
	if err := s.credStore.Store(ctx, key, payload, s.settings.Security.CredentialCacheTTL); err != nil {
		return errors.Wrap(err, "store credential envelope")
	}
	err = s.reactivateSummaryRefreshJobs(ctx, tx, ref)
	if err != nil {
		return err
	}
	return nil
}
