package files

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// Write applies content updates to a file path.
func (s *Service) Write(ctx context.Context, auth AuthContext, project, path, content, encoding string, offset int64, mode WriteMode) (WriteResult, error) {
	return s.WriteWith(ctx, auth, project, path, content, encoding, offset, mode, WriteOpts{})
}

// WriteWith applies content using the supplied WriteOpts (proposal §2.6.1, §4.2).
// The plain Write entry point delegates here with a zero-valued opts to preserve
// existing behavior.
func (s *Service) WriteWith(ctx context.Context, auth AuthContext, project, path, content, encoding string, offset int64, mode WriteMode, opts WriteOpts) (WriteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidPath, "path is required", false))
	}
	if offset < 0 {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "offset must be >= 0", false))
	}
	if _, err := NormalizeContentEncoding(encoding); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if mode == "" {
		mode = WriteModeAppend
	}
	if mode != WriteModeAppend && mode != WriteModeOverwrite && mode != WriteModeTruncate {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "invalid write mode", false))
	}
	if mode == WriteModeTruncate && offset != 0 {
		return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidOffset, "truncate requires offset 0", false))
	}

	payloadBytes := int64(len([]byte(content)))
	if err := ValidatePayloadSize(payloadBytes, s.settings.MaxPayloadBytes); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}

	if opts.SystemOwner != "" {
		ctx = contextWithSystemOwner(ctx, opts.SystemOwner)
	}

	var bytesWritten int64
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		n, err := s.writeWithinTx(ctx, tx, auth, project, path, []byte(content), mode, offset, payloadBytes, opts)
		if err != nil {
			return err
		}
		bytesWritten = n
		return nil
	})
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}

	return WriteResult{BytesWritten: bytesWritten}, nil
}

// writeWithinTx executes the write pipeline assuming the project lock is held.
// All snapshot, file upsert, prune, index-job, and credential-store steps live here
// so that callers such as RestoreVersion can reuse the same atomic flow.
func (s *Service) writeWithinTx( //nolint:gocognit // write involves multiple validation and upsert steps
	ctx context.Context,
	tx *sql.Tx,
	auth AuthContext,
	project, path string,
	content []byte,
	mode WriteMode,
	offset int64,
	bytesWritten int64,
	opts WriteOpts,
) (int64, error) {
	owner := systemOwnerFromContext(ctx)
	if opts.SystemOwner != "" {
		owner = opts.SystemOwner
	}

	if err := s.ensureNoDescendantFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
		return 0, err
	}
	if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
		return 0, err
	}

	existing, findErr := s.findActiveFileTx(ctx, tx, auth.APIKeyHash, project, path)
	if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
		return 0, errors.Wrap(findErr, "query existing file")
	}

	now := s.clock()
	var (
		newContent []byte
		createdAt  time.Time
		err        error
	)
	switch {
	case errors.Is(findErr, sql.ErrNoRows):
		createdAt = now
		newContent, err = applyWriteModeBytes(nil, content, offset, mode)
	default:
		createdAt = existing.CreatedAt
		newContent, err = applyWriteModeBytes(existing.Content, content, offset, mode)
	}
	if err != nil {
		return 0, err
	}

	newSize := int64(len(newContent))
	if err := ValidateFileSize(newSize, s.settings.MaxFileBytes); err != nil {
		return 0, err
	}
	if err := s.ensureProjectQuota(ctx, tx, auth.APIKeyHash, project, newSize, existing); err != nil {
		return 0, err
	}

	if errors.Is(findErr, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`INSERT INTO mcp_files (apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at, system_owner, skip_rag_index)
				VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, NULL, ?, ?)`, s.isPostgres),
			auth.APIKeyHash,
			project,
			path,
			newContent,
			newSize,
			createdAt,
			now,
			owner,
			opts.SkipRAGIndex,
		); err != nil {
			return 0, errors.Wrap(err, "create file")
		}
	} else {
		if err := s.snapshotFileVersionTx(ctx, tx, auth.APIKeyHash, project, path, existing.Content, existing.Size, existing.ID, now); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET content = ?, size = ?, updated_at = ?, deleted = FALSE, deleted_at = NULL, skip_rag_index = ? WHERE id = ? AND system_owner = ?`, s.isPostgres),
			newContent,
			newSize,
			now,
			opts.SkipRAGIndex,
			existing.ID,
			owner,
		); err != nil {
			return 0, errors.Wrap(err, "update file")
		}
		if err := s.pruneVersionsTx(ctx, tx, auth.APIKeyHash, project, path, now); err != nil {
			return 0, err
		}
	}

	// Index-job enqueue is gated on user-namespace writes that did not opt out.
	// System-owner writes never enqueue: their content is consumed by the owning
	// plugin directly, not by the rag index worker.
	if owner == "" && !opts.SkipRAGIndex {
		if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
			APIKeyHash:    auth.APIKeyHash,
			Project:       project,
			FilePath:      path,
			Operation:     "UPSERT",
			FileUpdatedAt: &now,
			Status:        "pending",
			RetryCount:    0,
			AvailableAt:   now,
			CreatedAt:     now,
			UpdatedAt:     now,
		}); err != nil {
			return 0, errors.Wrap(err, "enqueue index job")
		}
	}

	if owner == "" {
		if err := s.storeCredentialEnvelope(ctx, auth, project, path, now); err != nil {
			return 0, err
		}
	}

	return bytesWritten, nil
}

// Delete removes a file or directory tree.
func (s *Service) Delete(ctx context.Context, auth AuthContext, project, path string, recursive bool) (DeleteResult, error) {
	if err := s.validateAuth(auth); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	if path == "" {
		return DeleteResult{}, errors.WithStack(NewError(ErrCodePermissionDenied, "root directory cannot be deleted", false))
	}

	owner := systemOwnerFromContext(ctx)
	var deletedCount int
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		now := s.clock()
		paths, err := s.resolveDeleteTargets(ctx, tx, auth.APIKeyHash, project, path, recursive)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return errors.WithStack(NewError(ErrCodeNotFound, "path not found", false))
		}

		snapshots, err := s.loadFilesForSnapshotTx(ctx, tx, auth.APIKeyHash, project, paths)
		if err != nil {
			return err
		}
		for _, snap := range snapshots {
			if err := s.snapshotFileVersionTx(ctx, tx, auth.APIKeyHash, project, snap.Path, snap.Content, snap.Size, snap.ID, now); err != nil {
				return err
			}
		}

		query := rebindSQL(`UPDATE mcp_files SET deleted = TRUE, deleted_at = ?, updated_at = ? WHERE apikey_hash = ? AND project = ? AND deleted = FALSE AND system_owner = ? AND path IN (%s)`, s.isPostgres)
		inClause, inArgs := buildInClause(paths, s.isPostgres, 6)
		args := make([]any, 0, 5+len(inArgs))
		args = append(args, now, now, auth.APIKeyHash, project, owner)
		args = append(args, inArgs...)
		if _, err := tx.ExecContext(ctx, strings.Replace(query, "%s", inClause, 1), args...); err != nil {
			return errors.Wrap(err, "soft delete files")
		}

		for _, snap := range snapshots {
			if err := s.pruneVersionsTx(ctx, tx, auth.APIKeyHash, project, snap.Path, now); err != nil {
				return err
			}
		}

		if owner == "" {
			for _, p := range paths {
				if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
					APIKeyHash:    auth.APIKeyHash,
					Project:       project,
					FilePath:      p,
					Operation:     "DELETE",
					FileUpdatedAt: &now,
					Status:        "pending",
					RetryCount:    0,
					AvailableAt:   now,
					CreatedAt:     now,
					UpdatedAt:     now,
				}); err != nil {
					return errors.Wrap(err, "enqueue delete job")
				}
			}
		}

		deletedCount = len(paths)
		return nil
	})
	if err != nil {
		return DeleteResult{}, errors.WithStack(err)
	}
	return DeleteResult{DeletedCount: deletedCount}, nil
}

// applyWriteMode merges incoming content with existing data and returns new bytes.
func applyWriteMode(existing []byte, content string, offset int64, mode WriteMode) ([]byte, error) {
	return applyWriteModeBytes(existing, []byte(content), offset, mode)
}

// applyWriteModeBytes merges raw incoming bytes with existing data per write mode.
func applyWriteModeBytes(existing, incoming []byte, offset int64, mode WriteMode) ([]byte, error) {
	switch mode {
	case WriteModeAppend:
		return append(existing, incoming...), nil
	case WriteModeTruncate:
		return append([]byte{}, incoming...), nil
	case WriteModeOverwrite:
		size := int64(len(existing))
		if offset > size {
			return nil, NewError(ErrCodeInvalidOffset, "offset beyond eof", false)
		}
		newLen := int64(len(incoming)) + offset
		if newLen < size {
			newLen = size
		}
		buf := make([]byte, newLen)
		copy(buf, existing)
		copy(buf[offset:], incoming)
		return buf, nil
	default:
		return nil, NewError(ErrCodeInvalidOffset, "unsupported write mode", false)
	}
}

// ensureNoDescendantFile validates that the target path has no child files.
func (s *Service) ensureNoDescendantFile(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) error {
	owner := systemOwnerFromContext(ctx)
	prefix := buildPathPrefix(path)
	var count int64
	if err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE ? AND deleted = FALSE AND system_owner = ?`, s.isPostgres),
		apiKeyHash,
		project,
		prefix,
		owner,
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
		apiKeyHash,
		project,
		owner,
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
		apiKeyHash,
		project,
		path,
		owner,
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
		apiKeyHash,
		project,
		prefix,
		owner,
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
		apiKeyHash,
		project,
		owner,
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
		rebindSQL(`INSERT INTO mcp_file_index_jobs (apikey_hash, project, file_path, operation, file_updated_at, status, retry_count, available_at, created_at, updated_at, system_owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.isPostgres),
		job.APIKeyHash,
		job.Project,
		job.FilePath,
		job.Operation,
		job.FileUpdatedAt,
		job.Status,
		job.RetryCount,
		job.AvailableAt,
		job.CreatedAt,
		job.UpdatedAt,
		owner,
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

// findActiveFileTx loads one non-deleted file row in a transaction by path.
func (s *Service) findActiveFileTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string) (*File, error) {
	owner := systemOwnerFromContext(ctx)
	var file File
	err := tx.QueryRowContext(ctx,
		rebindSQL(`SELECT id, apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at
		FROM mcp_files
		WHERE apikey_hash = ? AND project = ? AND path = ? AND deleted = FALSE AND system_owner = ?
		LIMIT 1`, s.isPostgres),
		apiKeyHash,
		project,
		path,
		owner,
	).Scan(
		&file.ID,
		&file.APIKeyHash,
		&file.Project,
		&file.Path,
		&file.Content,
		&file.Size,
		&file.CreatedAt,
		&file.UpdatedAt,
		&file.Deleted,
		&file.DeletedAt,
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
