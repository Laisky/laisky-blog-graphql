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

// ListVersions returns versions for a path, newest first. Content is omitted.
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
	owner := systemOwnerFromContext(ctx)
	layers := historyProjects(ctx, project)
	args := []any{auth.APIKeyHash, path, owner}
	for _, layer := range layers {
		args = append(args, layer)
	}
	rows, err := s.db.QueryContext(ctx,
		rebindSQL(`SELECT id, size, created_at, source_file_id FROM mcp_file_versions
   WHERE apikey_hash = ? AND path = ? AND system_owner = ? AND project IN (`+placeholders(len(layers))+`) ORDER BY created_at DESC, id DESC`, s.isPostgres),
		args...,
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
		parsedAt, parseErr := parseDBTime(createdAt)
		if parseErr != nil {
			return nil, errors.Wrap(parseErr, "parse version created_at")
		}
		row.CreatedAt, row.APIKeyHash, row.Project, row.Path = parsedAt, auth.APIKeyHash, project, path
		if sourceID.Valid {
			if sourceID.Int64 < 0 {
				return nil, errors.New("invalid negative source file ID")
			}
			id := uint64(sourceID.Int64)
			row.SourceFileID = &id
		}
		versions = append(versions, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate file versions")
	}
	return versions, nil
}

// ReadVersion returns the full content of one immutable historical version.
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
	var row FileVersion
	var createdAt any
	var sourceID sql.NullInt64
	err := s.versionSnapshotRow(ctx, s.db, auth.APIKeyHash, project, path, versionID, "id, content, size, created_at, source_file_id").
		Scan(&row.ID, &row.Content, &row.Size, &createdAt, &sourceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FileVersion{}, errors.WithStack(NewError(ErrCodeNotFound, "version not found", false))
		}
		return FileVersion{}, errors.Wrap(err, "query file version")
	}
	parsedAt, parseErr := parseDBTime(createdAt)
	if parseErr != nil {
		return FileVersion{}, errors.Wrap(parseErr, "parse version created_at")
	}
	row.CreatedAt, row.APIKeyHash, row.Project, row.Path = parsedAt, auth.APIKeyHash, project, path
	if sourceID.Valid {
		if sourceID.Int64 < 0 {
			return FileVersion{}, errors.New("invalid negative source file ID")
		}
		id := uint64(sourceID.Int64)
		row.SourceFileID = &id
	}
	return row, nil
}

// RestoreVersion writes historical bytes as a new live revision. An optional
// restore precondition protects the CURRENT file, not the historical snapshot ID.
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
		// History is read through the same layers as file_list_versions, so a
		// default's snapshot can be restored through any project.
		var content []byte
		var size int64
		err := s.versionSnapshotRow(ctx, tx, auth.APIKeyHash, project, path, versionID, "content, size").Scan(&content, &size)
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

// historyProjects returns the physical projects whose history a project path
// shows: an overlaid project also shows DefaultProject history for the path.
func historyProjects(ctx context.Context, project string) []string {
	if overlayApplies(ctx, project) {
		return []string{project, DefaultProject}
	}
	return []string{project}
}

// versionSnapshotRow selects columns of one immutable snapshot of path that the
// project's history shows. Snapshot IDs are unique across projects.
func (s *Service) versionSnapshotRow(ctx context.Context, q sqlQueryer, apiKeyHash, project, path string, versionID uint64, columns string) *sql.Row {
	layers := historyProjects(ctx, project)
	args := make([]any, 0, 4+len(layers))
	args = append(args, apiKeyHash, path, versionID, systemOwnerFromContext(ctx))
	for _, layer := range layers {
		args = append(args, layer)
	}
	return q.QueryRowContext(ctx,
		rebindSQL(`SELECT `+columns+` FROM mcp_file_versions
			WHERE apikey_hash = ? AND path = ? AND id = ? AND system_owner = ? AND project IN (`+placeholders(len(layers))+`) LIMIT 1`, s.isPostgres),
		args...,
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
