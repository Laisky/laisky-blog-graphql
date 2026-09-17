package files

import (
	"context"
	"database/sql"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// ensureNoDescendantFile validates that the target path has no child files.
func (s *Service) ensureNoDescendantFile(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) error {
	owner := systemOwnerFromContext(ctx)
	prefix := buildPathPrefix(path)
	var count int64
	if err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash, project, prefix, owner,
	).Scan(&count); err != nil {
		return errors.Wrap(err, "check descendant files")
	}
	if count > 0 {
		return NewError(ErrCodeIsDirectory, "path has descendant files", false)
	}
	return nil
}

// ensureNoParentFile validates that no parent segment is an existing file.
func (s *Service) ensureNoParentFile(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) error {
	owner := systemOwnerFromContext(ctx)
	parents := parentPaths(path)
	if len(parents) == 0 {
		return nil
	}
	var count int64
	inClause, inArgs := buildInClause(parents, s.isPostgres, 4)
	query := rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)`, s.isPostgres)
	args := make([]any, 0, 3+len(inArgs))
	args = append(args, apiKeyHash, project, owner)
	args = append(args, inArgs...)
	if err := tx.QueryRowContext(ctx, strings.Replace(query, "%s", inClause, 1), args...).Scan(&count); err != nil {
		return errors.Wrap(err, "check parent files")
	}
	if count > 0 {
		return NewError(ErrCodeNotDirectory, "parent path is a file", false)
	}
	return nil
}

// parentPaths returns all parent segments for a path.
func parentPaths(path string) []string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) <= 1 {
		return nil
	}
	parents := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		parents = append(parents, "/"+strings.Join(parts[:i], "/"))
	}
	return parents
}

// ensureProjectQuota enforces project storage limits.
func (s *Service) ensureProjectQuota(ctx context.Context, tx *sql.Tx, apiKeyHash, project string, newSize int64, existing *File) error {
	owner := systemOwnerFromContext(ctx)
	var total int64
	if err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT COALESCE(SUM(size), 0) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash, project, owner,
	).Scan(&total); err != nil {
		return errors.Wrap(err, "sum project size")
	}
	if existing != nil {
		total -= existing.Size
	}
	if total+newSize > s.settings.MaxProjectBytes {
		return NewError(ErrCodeQuotaExceeded, "project storage quota exceeded", false)
	}
	return nil
}

// resolveDeleteTargets determines which file paths should be deleted.
func (s *Service) resolveDeleteTargets(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, recursive bool) ([]string, error) {
	owner := systemOwnerFromContext(ctx)
	if path == "" {
		return s.listAllFilePaths(ctx, tx, apiKeyHash, project)
	}
	var foundPath string
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT path FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ? LIMIT 1`, s.isPostgres),
		apiKeyHash, project, path, owner,
	).Scan(&foundPath)
	if err == nil {
		return []string{foundPath}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, errors.Wrap(err, "query delete target")
	}
	paths, err := s.listDescendantPaths(ctx, tx, apiKeyHash, project, path)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if !recursive {
		return nil, NewError(ErrCodeNotEmpty, "directory not empty", false)
	}
	return paths, nil
}

// listDescendantPaths returns all active descendant file paths for a directory.
func (s *Service) listDescendantPaths(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) ([]string, error) {
	owner := systemOwnerFromContext(ctx)
	prefix := buildPathPrefix(path)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT path FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash, project, prefix, owner,
	)
	if err != nil {
		return nil, errors.Wrap(err, "query descendant paths")
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if scanErr := rows.Scan(&p); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan descendant path")
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate descendant paths")
	}
	return paths, nil
}

// listAllFilePaths returns all active file paths in a project.
func (s *Service) listAllFilePaths(ctx context.Context, tx *sql.Tx, apiKeyHash, project string) ([]string, error) {
	owner := systemOwnerFromContext(ctx)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT path FROM mcp_files WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash, project, owner,
	)
	if err != nil {
		return nil, errors.Wrap(err, "query project paths")
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if scanErr := rows.Scan(&p); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan project path")
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate project paths")
	}
	return paths, nil
}

// insertIndexJobTx inserts one index queue job in the current transaction.
func (s *Service) insertIndexJobTx(ctx context.Context, tx *sql.Tx, job FileIndexJob) error {
	owner := systemOwnerFromContext(ctx)
	_, err := tx.ExecContext(ctx,
		rebindSQL(`INSERT INTO mcp_file_index_jobs (apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at, system_owner, content_hash, last_error_code, summary_generation_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.isPostgres),
		job.APIKeyHash, job.Project, job.FilePath, job.Operation, job.FileUpdatedAt,
		job.Status, job.RetryCount, job.AvailableAt, job.CreatedAt, job.UpdatedAt,
		owner, job.ContentHash, job.LastErrorCode, job.SummaryGenerationKey,
	)
	if err != nil {
		return errors.Wrap(err, "insert index job")
	}
	return nil
}

// loadFilesForSnapshotTx loads non-deleted file rows in batch for snapshotting.
func (s *Service) loadFilesForSnapshotTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project string, paths []string) ([]File, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	owner := systemOwnerFromContext(ctx)
	inClause, inArgs := buildInClause(paths, s.isPostgres, 4)
	query := rebindSQL(`SELECT id, path, content, size FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)
		ORDER BY path ASC`, s.isPostgres)
	args := make([]any, 0, 3+len(inArgs))
	args = append(args, apiKeyHash, project, owner)
	args = append(args, inArgs...)
	rows, err := tx.QueryContext(ctx, strings.Replace(query, "%s", inClause, 1), args...)
	if err != nil {
		return nil, errors.Wrap(err, "query files for snapshot")
	}
	defer func() { _ = rows.Close() }()
	var files []File
	for rows.Next() {
		var file File
		if scanErr := rows.Scan(&file.ID, &file.Path, &file.Content, &file.Size); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan file for snapshot")
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate files for snapshot")
	}
	return files, nil
}

// findActiveFileTx loads one non-deleted file row and its generation in a transaction.
func (s *Service) findActiveFileTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (*File, error) {
	owner := systemOwnerFromContext(ctx)
	var file File
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT id, apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at,
			content_hash, summary_content_hash, summary_status, summary_generation_key, incarnation_id || ':' || CAST(revision AS TEXT)
		FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?
		LIMIT 1`, s.isPostgres),
		apiKeyHash, project, path, owner,
	).Scan(
		&file.ID, &file.APIKeyHash, &file.Project, &file.Path, &file.Content,
		&file.Size, &file.CreatedAt, &file.UpdatedAt, &file.Deleted, &file.DeletedAt,
		&file.ContentHash, &file.SummaryContentHash, &file.SummaryStatus, &file.SummaryGenerationKey, &file.Version,
	)
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// buildInClause returns a placeholder list and positional args for IN clauses.
func buildInClause(values []string, isPostgres bool, startIndex int) (string, []any) {
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
