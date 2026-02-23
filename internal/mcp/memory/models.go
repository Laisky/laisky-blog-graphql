package memory

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
)

const (
	turnGuardStatusProcessing = "processing"
	turnGuardStatusDone       = "done"
)

// TurnGuard stores idempotency state for one committed memory turn.
type TurnGuard struct {
	ID         int64
	APIKeyHash string
	Project    string
	SessionID  string
	TurnID     string
	Status     string
	UpdatedAt  time.Time
	CreatedAt  time.Time
}

// runMigrations ensures required memory tables exist.
func runMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("sql db is required")
	}

	if isPostgresDB(db) {
		if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS turn_guards (
	id BIGSERIAL PRIMARY KEY,
	api_key_hash CHAR(64) NOT NULL,
	project VARCHAR(128) NOT NULL,
	session_id VARCHAR(256) NOT NULL,
	turn_id VARCHAR(256) NOT NULL,
	status VARCHAR(32) NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
)`); err != nil {
			return errors.Wrap(err, "create turn_guards table")
		}
	} else {
		if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS turn_guards (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	api_key_hash TEXT NOT NULL,
	project TEXT NOT NULL,
	session_id TEXT NOT NULL,
	turn_id TEXT NOT NULL,
	status TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL
)`); err != nil {
			return errors.Wrap(err, "create turn_guards table")
		}
	}

	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_memory_turn_guard_key ON turn_guards (api_key_hash, project, session_id, turn_id)`); err != nil {
		return errors.Wrap(err, "create idx_mcp_memory_turn_guard_key")
	}

	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_turn_guards_updated_at ON turn_guards (updated_at)`); err != nil {
		return errors.Wrap(err, "create idx_turn_guards_updated_at")
	}

	return nil
}

// isUniqueConstraintError reports whether the error indicates a unique key conflict.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrConstraint || sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
	}

	return false
}
