package files

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
)

// Write applies content updates to a file path.
func (s *Service) Write(ctx context.Context, auth AuthContext, project, path, content, encoding string, offset int64, mode WriteMode) (WriteResult, error) {
	return s.WriteWith(ctx, auth, project, path, content, encoding, offset, mode, WriteOpts{})
}

// WriteWith applies content and optional version conditions atomically. Zero-valued
// options retain legacy blind writes; every accepted write still advances revision.
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

	payloadBytes := int64(len(content))
	if err := ValidatePayloadSize(payloadBytes, s.settings.MaxPayloadBytes); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if opts.SystemOwner != "" {
		ctx = contextWithSystemOwner(ctx, opts.SystemOwner)
	}
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationWrite)
	if opts.ExpectedVersion != "" || opts.CreateOnly {
		if !conditions.Empty() && (conditions.ExpectedVersion != opts.ExpectedVersion || conditions.CreateOnly != opts.CreateOnly) {
			return WriteResult{}, errors.WithStack(NewError(ErrCodeInvalidArgument, "conflicting write conditions", false))
		}
		conditions.ExpectedVersion, conditions.CreateOnly = opts.ExpectedVersion, opts.CreateOnly
	}
	if !conditions.Empty() {
		if _, err := WithFilePreconditions(ctx, auth, project, path, FileOperationWrite, conditions); err != nil {
			return WriteResult{}, errors.WithStack(err)
		}
	}
	opts.ExpectedVersion, opts.CreateOnly = conditions.ExpectedVersion, conditions.CreateOnly

	var result WriteResult
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		n, err := s.writeWithinTx(ctx, tx, auth, project, path, []byte(content), mode, offset, payloadBytes, opts)
		if err != nil {
			return err
		}
		version, err := s.fileVersionTx(ctx, tx, auth, project, path)
		if err != nil {
			return err
		}
		result = WriteResult{BytesWritten: n, Version: version}
		return nil
	})
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	return result, nil
}

// writeWithinTx executes the write pipeline assuming the project lock is held.
// Preconditions, content, historical preimage, and indexing outbox share this
// transaction. The database trigger increments the revision on every write path.
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

	existing, findErr := s.findActiveFileTx(ctx, tx, auth.APIKeyHash, project, path)
	if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
		return 0, errors.Wrap(findErr, "query existing file")
	}
	actualVersion := ""
	if existing != nil {
		actualVersion = existing.Version
	}
	if err := checkFileVersion(actualVersion, opts.ExpectedVersion, opts.CreateOnly); err != nil {
		return 0, err
	}
	if err := s.ensureNoDescendantFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
		return 0, err
	}
	if err := s.ensureNoParentFile(ctx, tx, auth.APIKeyHash, project, path); err != nil {
		return 0, err
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

	// Hash the complete generation, never just an APPEND/OVERWRITE request delta.
	contentHash := HashFileContent(newContent)
	if errors.Is(findErr, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			rebindSQL(`INSERT INTO mcp_files (apikey_hash, project, path, content, size, created_at, updated_at, deleted, deleted_at, system_owner, skip_rag_index, content_hash)
				VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, NULL, ?, ?, ?)`, s.isPostgres),
			auth.APIKeyHash, project, path, newContent, newSize, createdAt, now, owner, opts.SkipRAGIndex, contentHash,
		); err != nil {
			return 0, errors.Wrap(err, "create file")
		}
	} else {
		if err := s.snapshotFileVersionTx(ctx, tx, auth.APIKeyHash, project, path, existing.Content, existing.Size, existing.ID, now); err != nil {
			return 0, err
		}
		// Preserve the previous summary until its replacement publishes. The
		// generation predicate is defense in depth in addition to the project lock.
		updated, err := tx.ExecContext(ctx,
			rebindSQL(`UPDATE mcp_files SET content = ?, size = ?, updated_at = ?, deleted = FALSE, deleted_at = NULL, skip_rag_index = ?, content_hash = ?
				WHERE id = ? AND apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE
				AND (incarnation_id || ':' || CAST(revision AS TEXT)) = ?`, s.isPostgres),
			newContent, newSize, now, opts.SkipRAGIndex, contentHash,
			existing.ID, auth.APIKeyHash, project, path, owner, existing.Version,
		)
		if err != nil {
			return 0, errors.Wrap(err, "update file")
		}
		n, err := updated.RowsAffected()
		if err != nil {
			return 0, errors.Wrap(err, "check conditional file update")
		}
		if n != 1 {
			return 0, NewError(ErrCodeVersionConflict, "file changed during mutation; re-read and recompute the edit", false)
		}
		if err := s.pruneVersionsTx(ctx, tx, auth.APIKeyHash, project, path, now); err != nil {
			return 0, err
		}
	}

	// Only user-namespace writes that did not opt out enqueue RAG work.
	if owner == "" && !opts.SkipRAGIndex {
		if err := s.insertIndexJobTx(ctx, tx, FileIndexJob{
			APIKeyHash: auth.APIKeyHash, Project: project, FilePath: path,
			Operation: "UPSERT", FileUpdatedAt: &now, Status: "pending", RetryCount: 0,
			AvailableAt: now, CreatedAt: now, UpdatedAt: now, ContentHash: contentHash,
		}); err != nil {
			return 0, errors.Wrap(err, "enqueue index job")
		}
	}
	if owner == "" {
		if err := s.storeCredentialEnvelopeTx(ctx, tx, auth, project, path, now); err != nil {
			return 0, err
		}
	}
	return bytesWritten, nil
}

// Delete removes a file or directory tree. A supplied file token protects only
// an exact file; it can never authorize deleting an implicit directory subtree.
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
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationDelete)
	var deletedCount int
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		if err := s.checkPathVersionTx(ctx, tx, auth, project, path, conditions.ExpectedVersion, false); err != nil {
			return err
		}
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
					APIKeyHash: auth.APIKeyHash, Project: project, FilePath: p,
					Operation: "DELETE", FileUpdatedAt: &now, Status: "pending", RetryCount: 0,
					AvailableAt: now, CreatedAt: now, UpdatedAt: now,
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

// applyWriteModeBytes merges complete UTF-8 units and rejects corrupt results.
func applyWriteModeBytes(existing, incoming []byte, offset int64, mode WriteMode) ([]byte, error) {
	if offset < 0 {
		return nil, NewError(ErrCodeInvalidOffset, "offset must be >= 0", false)
	}
	if !utf8.Valid(incoming) {
		return nil, NewError(ErrCodeInvalidContent, "content must be valid UTF-8", false)
	}
	var result []byte
	switch mode {
	case WriteModeAppend:
		result = append(existing, incoming...)
	case WriteModeTruncate:
		result = append([]byte{}, incoming...)
	case WriteModeOverwrite:
		size := int64(len(existing))
		if offset > size || int64(len(incoming)) > math.MaxInt64-offset {
			return nil, NewError(ErrCodeInvalidOffset, "offset beyond eof or range overflow", false)
		}
		end := offset + int64(len(incoming))
		if (offset < size && !utf8.RuneStart(existing[offset])) || (end < size && !utf8.RuneStart(existing[end])) {
			return nil, NewError(ErrCodeInvalidOffset, "overwrite range splits a UTF-8 code point", false)
		}
		result = make([]byte, max(end, size))
		copy(result, existing)
		copy(result[offset:], incoming)
	default:
		return nil, NewError(ErrCodeInvalidOffset, "unsupported write mode", false)
	}
	if result == nil {
		result = []byte{}
	}
	if !utf8.Valid(result) {
		return nil, NewError(ErrCodeInvalidContent, "write would produce invalid UTF-8", false)
	}
	return result, nil
}
