package files

import (
	"context"
	"database/sql"

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
		indexStatusPending,
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
