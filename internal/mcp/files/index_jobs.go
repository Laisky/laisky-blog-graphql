package files

import (
	"context"
	"database/sql"

	errors "github.com/Laisky/errors/v2"
)

// reactivateSummaryRefreshJobs moves waiting refresh jobs back to the runnable
// state after a fresh credential envelope is stored, attaching its exact ID.
// An already claimed job keeps its claim but records the fresh ID. A worker
// holding the old ID cannot park that job after fresh authorization wins.
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
		rebindSQL(`UPDATE mcp_file_index_jobs
		SET status = CASE WHEN status = 'waiting_auth' THEN ? ELSE status END,
		    available_at = CASE WHEN status = 'waiting_auth' THEN ? ELSE available_at END,
		    last_error_code = CASE WHEN status = 'waiting_auth' THEN '' ELSE last_error_code END,
		    updated_at = ?, credential_id = ?
		WHERE apikey_hash = ? AND project = ? AND file_path = ? AND operation = ? AND system_owner = ?
		      AND status IN ('waiting_auth', 'processing') AND content_hash = ? AND summary_generation_key = ?`, s.isPostgres),
		indexStatusPending, now, now, ref.ID, ref.APIKeyHash, ref.Project, ref.Path,
		"SUMMARY_REFRESH", "", contentHash, generationKey,
	)
	return err
}

type sqlQueryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
