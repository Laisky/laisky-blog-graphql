package files

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// HistoryEntry exposes immutable snapshot metadata without lossy numeric JSON IDs.
type HistoryEntry struct {
	ID        string    `json:"id"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// HistoryPage is a bounded, ID-ordered page of immutable snapshots, newest first.
type HistoryPage struct {
	Versions   []HistoryEntry `json:"versions"`
	HasMore    bool           `json:"has_more"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// HistoryReader is shared by external history adapters, independent of MCP registration.
type HistoryReader interface {
	ListVersionPage(context.Context, AuthContext, string, string, uint64, int) (HistoryPage, error)
	ReadVersion(context.Context, AuthContext, string, string, uint64) (FileVersion, error)
}

// ParseHistoryID validates a canonical positive ID representable by a database BIGINT.
// A historical ID selects immutable bytes and is never a live file version token.
func ParseHistoryID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 63)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != raw {
		return 0, errors.WithStack(NewError(ErrCodeInvalidArgument, "history ID must be a positive canonical decimal string", false))
	}
	return id, nil
}

// ListVersionPage returns bounded, tenant/project/owner-scoped historical metadata.
// Use the last page's next_cursor as beforeID; newly created rows cannot shift an older page.
func (s *Service) ListVersionPage(ctx context.Context, auth AuthContext, project, path string, beforeID uint64, limit int) (HistoryPage, error) {
	if err := s.validateAuth(auth); err != nil {
		return HistoryPage{}, errors.WithStack(err)
	}
	if err := ValidateProject(project); err != nil {
		return HistoryPage{}, errors.WithStack(err)
	}
	if err := ValidatePath(path); err != nil {
		return HistoryPage{}, errors.WithStack(err)
	}
	if path == "" || limit < 1 || limit > 200 || beforeID > uint64(1<<63-1) {
		return HistoryPage{}, errors.WithStack(NewError(ErrCodeInvalidArgument, "history requires a file path, limit 1..200 and a valid cursor", false))
	}
	query := `SELECT id, size, created_at FROM mcp_file_versions
		WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`
	args := []any{auth.APIKeyHash, project, path, systemOwnerFromContext(ctx)}
	if beforeID != 0 {
		query += ` AND id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, rebindSQL(query, s.isPostgres), args...)
	if err != nil {
		return HistoryPage{}, errors.Wrap(err, "query history page")
	}
	defer func() { _ = rows.Close() }()
	return scanHistoryPage(rows, limit)
}

// scanHistoryPage keeps cursor comparison and wire IDs exact above JavaScript's safe range.
func scanHistoryPage(rows *sql.Rows, limit int) (HistoryPage, error) {
	page := HistoryPage{Versions: make([]HistoryEntry, 0, limit)}
	for rows.Next() {
		var id uint64
		var size int64
		var created any
		if err := rows.Scan(&id, &size, &created); err != nil {
			return HistoryPage{}, errors.Wrap(err, "scan history page")
		}
		if len(page.Versions) == limit {
			page.HasMore = true
			page.NextCursor = page.Versions[len(page.Versions)-1].ID
			break
		}
		createdAt, err := parseDBTime(created)
		if err != nil {
			return HistoryPage{}, errors.Wrap(err, "parse history timestamp")
		}
		page.Versions = append(page.Versions, HistoryEntry{ID: strconv.FormatUint(id, 10), Size: size, CreatedAt: createdAt.UTC()})
	}
	if err := rows.Err(); err != nil {
		return HistoryPage{}, errors.Wrap(err, "iterate history page")
	}
	return page, nil
}
