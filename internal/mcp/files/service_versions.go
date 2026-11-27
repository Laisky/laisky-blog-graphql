package files

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// versionRetentionWindow is the time window during which all versions are kept.
const versionRetentionWindow = 7 * 24 * time.Hour

// versionRetentionTopN is the minimum number of latest versions to keep regardless of age.
const versionRetentionTopN = 3

// ListVersions returns the history of the file a path of project addresses,
// newest first. Content is omitted. The path resolves like a normal read, and
// only that file's physical project is listed (see fileNamespaceQ), so each
// FileVersion names the project that stores the snapshot.
func (s *Service) ListVersions(ctx context.Context, auth AuthContext, project, path string) ([]FileVersion, error) {
	if err := s.validateAuth(auth); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return nil, errors.WithStack(err)
	}
	if path == "" {
		return nil, errors.WithStack(NewError(ErrCodeInvalidPath, "path is required", false))
	}
	overlay := overlayApplies(ctx, project)
	var versions []FileVersion
	err := s.withReadSnapshot(ctx, overlay, func(q sqlQueryer) error {
		physical, _, err := s.fileNamespaceQ(ctx, q, auth.APIKeyHash, project, path, overlay)
		if err != nil {
			return err
		}
		versions, err = s.listVersionsQ(ctx, q, auth.APIKeyHash, physical, path)
		return err
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return versions, nil
}

// listVersionsQ lists the snapshot metadata of one physical project path.
func (s *Service) listVersionsQ(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string) ([]FileVersion, error) {
	rows, err := q.QueryContext(ctx,
		rebindSQL(`SELECT id, size, created_at, source_file_id FROM mcp_file_versions
   WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? ORDER BY created_at DESC, id DESC`, s.isPostgres),
		apiKeyHash, project, path, systemOwnerFromContext(ctx),
	)
	if err != nil {
		return nil, errors.Wrap(err, "query file versions")
	}
	defer func() { _ = rows.Close() }()
	var versions []FileVersion
	for rows.Next() {
		var row FileVersion
		var createdAt any
		var sourceID sql.NullInt64
		if scanErr := rows.Scan(&row.ID, &row.Size, &createdAt, &sourceID); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan file version")
		}
		if err := fillFileVersion(&row, createdAt, sourceID); err != nil {
			return nil, err
		}
		row.APIKeyHash, row.Project, row.Path = apiKeyHash, project, path
		versions = append(versions, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate file versions")
	}
	return versions, nil
}

// ReadVersion returns the full content of one immutable snapshot of the file a
// path of project addresses. A snapshot ID of another physical project (for
// example the other layer of an overlaid path) is NOT_FOUND.
//
// A restore that reads history before a separate conditional write scopes its
// conditions to FileOperationRestore. ReadVersion then also checks them against
// the file it resolved, in the same snapshot. Version tokens name one physical
// row, so a write that later passes the same conditions addresses the same
// file and stores the snapshot in the namespace it was read from.
func (s *Service) ReadVersion(ctx context.Context, auth AuthContext, project, path string, versionID uint64) (FileVersion, error) {
	if err := s.validateAuth(auth); err != nil {
		return FileVersion{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return FileVersion{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return FileVersion{}, errors.WithStack(err)
	}
	if path == "" {
		return FileVersion{}, errors.WithStack(NewError(ErrCodeInvalidPath, "path is required", false))
	}
	overlay := overlayApplies(ctx, project)
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationRestore)
	checkConditions := overlay && !conditions.Empty()
	var row FileVersion
	err := s.withReadSnapshot(ctx, overlay, func(q sqlQueryer) error {
		physical, current, err := s.fileNamespaceQ(ctx, q, auth.APIKeyHash, project, path, overlay)
		if err != nil {
			return err
		}
		var createdAt any
		var sourceID sql.NullInt64
		err = s.versionSnapshotRow(ctx, q, auth.APIKeyHash, physical, path, versionID, "id, content, size, created_at, source_file_id").
			Scan(&row.ID, &row.Content, &row.Size, &createdAt, &sourceID)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.WithStack(NewError(ErrCodeNotFound, "version not found", false))
		}
		if err != nil {
			return errors.Wrap(err, "query file version")
		}
		if err := fillFileVersion(&row, createdAt, sourceID); err != nil {
			return err
		}
		row.APIKeyHash, row.Project, row.Path = auth.APIKeyHash, physical, path
		if !checkConditions {
			return nil
		}
		actual := ""
		if current != nil {
			actual = current.Version
		}
		return errors.WithStack(checkFileVersion(actual, conditions.ExpectedVersion, conditions.CreateOnly))
	})
	if err != nil {
		return FileVersion{}, errors.WithStack(err)
	}
	return row, nil
}

// fillFileVersion parses the created_at and source_file_id columns of a snapshot row.
func fillFileVersion(row *FileVersion, createdAt any, sourceID sql.NullInt64) error {
	parsedAt, err := parseDBTime(createdAt)
	if err != nil {
		return errors.Wrap(err, "parse version created_at")
	}
	row.CreatedAt = parsedAt
	if sourceID.Valid {
		if sourceID.Int64 < 0 {
			return errors.New("invalid negative source file ID")
		}
		id := uint64(sourceID.Int64)
		row.SourceFileID = &id
	}
	return nil
}

// RestoreVersion writes historical bytes as a new live revision. An optional
// restore precondition protects the CURRENT file, not the historical snapshot ID.
//
// Inside the locked mutation transaction the path resolves like a write: the
// condition is checked against the file the path shows, the target is that
// file's physical project (or the addressed project when none is visible), and
// the snapshot must belong to that same physical file history. A snapshot of
// another layer is NOT_FOUND and nothing changes.
func (s *Service) RestoreVersion(ctx context.Context, auth AuthContext, project, path string, versionID uint64) (WriteResult, error) {
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
	owner := systemOwnerFromContext(ctx)
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationRestore)
	var result WriteResult
	err := s.runUserMutation(ctx, auth.APIKeyHash, project, []string{project}, []string{path}, func(tx *sql.Tx, overlay bool) error {
		if err := s.checkVersionTx(ctx, tx, auth, project, path, conditions.ExpectedVersion, conditions.CreateOnly, overlay); err != nil {
			return err
		}
		layer := project
		if overlay {
			var resolveErr error
			if layer, resolveErr = s.resolveWriteLayerTx(ctx, tx, auth.APIKeyHash, project, path); resolveErr != nil {
				return resolveErr
			}
		}
		var content []byte
		var size int64
		err := s.versionSnapshotRow(ctx, tx, auth.APIKeyHash, layer, path, versionID, "content, size").Scan(&content, &size)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.WithStack(NewError(ErrCodeNotFound, "version not found", false))
			}
			return errors.Wrap(err, "load version for restore")
		}
		if err := ValidatePayloadSize(size, s.settings.MaxPayloadBytes); err != nil {
			return errors.WithStack(err)
		}
		n, err := s.writeWithinTx(ctx, tx, auth, layer, path, content, WriteModeTruncate, 0, size, WriteOpts{
			SystemOwner: owner, ExpectedVersion: conditions.ExpectedVersion, CreateOnly: conditions.CreateOnly,
		})
		if err != nil {
			return err
		}
		version, err := s.fileVersionTx(ctx, tx, auth, layer, path)
		if err != nil {
			return err
		}
		result = WriteResult{BytesWritten: n, Version: version, ResolvedProject: layer}
		return nil
	})
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	return result, nil
}

// versionSnapshotRow selects columns of one immutable snapshot of a path of
// one physical project. Snapshot IDs are unique across projects, so the
// project predicate is what rejects an ID of another namespace.
func (s *Service) versionSnapshotRow(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, versionID uint64, columns string) *sql.Row {
	return q.QueryRowContext(ctx,
		rebindSQL(`SELECT `+columns+` FROM mcp_file_versions
			WHERE apikey_hash = ? AND project = ? AND path = ? AND id = ? AND system_owner = ? LIMIT 1`, s.isPostgres),
		apiKeyHash, project, path, versionID, systemOwnerFromContext(ctx),
	)
}

// snapshotFileVersionTx inserts a snapshot row representing a file's prior content.
func (s *Service) snapshotFileVersionTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, content []byte, size int64, sourceID uint64, now time.Time) error {
	owner := systemOwnerFromContext(ctx)
	if _, err := tx.ExecContext(ctx,
		rebindSQL(`INSERT INTO mcp_file_versions (apikey_hash, project, path, content, size, created_at, source_file_id, system_owner)
   VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.isPostgres),
		apiKeyHash, project, path, content, size, now, sourceID, owner,
	); err != nil {
		return errors.Wrap(err, "insert file version snapshot")
	}
	return nil
}

// pruneVersionsTx applies the union retention rule (top N OR within retention window).
func (s *Service) pruneVersionsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, now time.Time) error {
	owner := systemOwnerFromContext(ctx)
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT id, created_at FROM mcp_file_versions
   WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? ORDER BY created_at DESC, id DESC`, s.isPostgres),
		apiKeyHash, project, path, owner,
	)
	if err != nil {
		return errors.Wrap(err, "query versions for prune")
	}
	defer func() { _ = rows.Close() }()
	type versionRow struct {
		id        uint64
		createdAt time.Time
	}
	var all []versionRow
	for rows.Next() {
		var id uint64
		var createdAt any
		if scanErr := rows.Scan(&id, &createdAt); scanErr != nil {
			_ = rows.Close()
			return errors.Wrap(scanErr, "scan version for prune")
		}
		parsedAt, parseErr := parseDBTime(createdAt)
		if parseErr != nil {
			_ = rows.Close()
			return errors.Wrap(parseErr, "parse version created_at for prune")
		}
		all = append(all, versionRow{id: id, createdAt: parsedAt})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errors.Wrap(err, "iterate versions for prune")
	}
	if err := rows.Close(); err != nil {
		return errors.Wrap(err, "close versions cursor for prune")
	}
	if len(all) == 0 {
		return nil
	}
	keep := make(map[uint64]struct{}, len(all))
	for i, row := range all {
		if i < versionRetentionTopN || now.Sub(row.createdAt) <= versionRetentionWindow {
			keep[row.id] = struct{}{}
		}
	}
	deleteIDs := make([]uint64, 0, len(all))
	for _, row := range all {
		if _, ok := keep[row.id]; !ok {
			deleteIDs = append(deleteIDs, row.id)
		}
	}
	if len(deleteIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(deleteIDs))
	args := make([]any, 0, 4+len(deleteIDs))
	args = append(args, apiKeyHash, project, path, owner)
	for _, id := range deleteIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := `DELETE FROM mcp_file_versions WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND id IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := tx.ExecContext(ctx, rebindSQL(query, s.isPostgres), args...); err != nil {
		return errors.Wrap(err, "delete pruned versions")
	}
	return nil
}
