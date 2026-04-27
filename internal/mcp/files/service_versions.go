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

	rows, err := s.db.QueryContext(ctx,
		rebindSQL(`SELECT id, size, created_at, source_file_id
			FROM mcp_file_versions
			WHERE apikey_hash = ? AND project = ? AND path = ?
			ORDER BY created_at DESC, id DESC`, s.isPostgres),
		auth.APIKeyHash,
		project,
		path,
	)
	if err != nil {
		return nil, errors.Wrap(err, "query file versions")
	}
	defer func() { _ = rows.Close() }()

	var versions []FileVersion
	for rows.Next() {
		var (
			row       FileVersion
			createdAt any
			sourceID  sql.NullInt64
		)
		if scanErr := rows.Scan(&row.ID, &row.Size, &createdAt, &sourceID); scanErr != nil {
			return nil, errors.Wrap(scanErr, "scan file version")
		}
		parsedAt, parseErr := parseDBTime(createdAt)
		if parseErr != nil {
			return nil, errors.Wrap(parseErr, "parse version created_at")
		}
		row.CreatedAt = parsedAt
		row.APIKeyHash = auth.APIKeyHash
		row.Project = project
		row.Path = path
		if sourceID.Valid {
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

// ReadVersion returns the full content of one version.
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

	var (
		row       FileVersion
		createdAt any
		sourceID  sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		rebindSQL(`SELECT id, content, size, created_at, source_file_id
			FROM mcp_file_versions
			WHERE apikey_hash = ? AND project = ? AND path = ? AND id = ?
			LIMIT 1`, s.isPostgres),
		auth.APIKeyHash,
		project,
		path,
		versionID,
	).Scan(&row.ID, &row.Content, &row.Size, &createdAt, &sourceID)
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
	row.CreatedAt = parsedAt
	row.APIKeyHash = auth.APIKeyHash
	row.Project = project
	row.Path = path
	if sourceID.Valid {
		id := uint64(sourceID.Int64)
		row.SourceFileID = &id
	}
	return row, nil
}

// RestoreVersion writes the content of versionID as the new current content.
// The version row is looked up inside the project lock and pinned to (apikey_hash,
// project, path, id), so concurrent renames or prunes cannot race the restore.
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

	var bytesWritten int64
	err := s.lockProvider.WithProjectLock(ctx, s.db, s.isPostgres, auth.APIKeyHash, project, s.settings.LockTimeout, func(tx *sql.Tx) error {
		var content []byte
		var size int64
		err := tx.QueryRowContext(ctx,
			rebindSQL(`SELECT content, size FROM mcp_file_versions
				WHERE apikey_hash = ? AND project = ? AND path = ? AND id = ?
				LIMIT 1`, s.isPostgres),
			auth.APIKeyHash,
			project,
			path,
			versionID,
		).Scan(&content, &size)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.WithStack(NewError(ErrCodeNotFound, "version not found", false))
			}
			return errors.Wrap(err, "load version for restore")
		}

		if err := ValidatePayloadSize(size, s.settings.MaxPayloadBytes); err != nil {
			return errors.WithStack(err)
		}

		n, err := s.writeWithinTx(ctx, tx, auth, project, path, content, WriteModeTruncate, 0, size)
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

// snapshotFileVersionTx inserts a snapshot row representing a file's prior content.
func (s *Service) snapshotFileVersionTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, content []byte, size int64, sourceID uint64, now time.Time) error {
	if _, err := tx.ExecContext(ctx,
		rebindSQL(`INSERT INTO mcp_file_versions (apikey_hash, project, path, content, size, created_at, source_file_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, s.isPostgres),
		apiKeyHash,
		project,
		path,
		content,
		size,
		now,
		sourceID,
	); err != nil {
		return errors.Wrap(err, "insert file version snapshot")
	}
	return nil
}

// pruneVersionsTx applies the union retention rule (top N OR within retention window).
func (s *Service) pruneVersionsTx(ctx context.Context, tx *sql.Tx, apiKeyHash, project, path string, now time.Time) error {
	rows, err := tx.QueryContext(ctx,
		rebindSQL(`SELECT id, created_at FROM mcp_file_versions
			WHERE apikey_hash = ? AND project = ? AND path = ?
			ORDER BY created_at DESC, id DESC`, s.isPostgres),
		apiKeyHash,
		project,
		path,
	)
	if err != nil {
		return errors.Wrap(err, "query versions for prune")
	}

	type versionRow struct {
		id        uint64
		createdAt time.Time
	}
	var all []versionRow
	for rows.Next() {
		var (
			id        uint64
			createdAt any
		)
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
		if i < versionRetentionTopN {
			keep[row.id] = struct{}{}
			continue
		}
		if now.Sub(row.createdAt) <= versionRetentionWindow {
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
	args := make([]any, 0, 3+len(deleteIDs))
	args = append(args, apiKeyHash, project, path)
	for _, id := range deleteIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	query := `DELETE FROM mcp_file_versions
		WHERE apikey_hash = ? AND project = ? AND path = ? AND id IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := tx.ExecContext(ctx, rebindSQL(query, s.isPostgres), args...); err != nil {
		return errors.Wrap(err, "delete pruned versions")
	}
	return nil
}
