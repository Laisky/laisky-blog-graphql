package memory

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

const (
	turnGuardStatusProcessing = "processing"
	turnGuardStatusDone       = "done"
)

// TurnGuard stores idempotency state for one committed memory turn.
type TurnGuard struct {
	ID         uint      `gorm:"primaryKey"`
	APIKeyHash string    `gorm:"type:char(64);not null;index:idx_mcp_memory_turn_guard_key,unique"`
	Project    string    `gorm:"type:varchar(128);not null;index:idx_mcp_memory_turn_guard_key,unique"`
	SessionID  string    `gorm:"type:varchar(256);not null;index:idx_mcp_memory_turn_guard_key,unique"`
	TurnID     string    `gorm:"type:varchar(256);not null;index:idx_mcp_memory_turn_guard_key,unique"`
	Status     string    `gorm:"type:varchar(32);not null"`
	UpdatedAt  time.Time `gorm:"not null;index"`
	CreatedAt  time.Time `gorm:"not null"`
}

// runMigrations ensures required memory tables exist.
func runMigrations(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("gorm db is required")
	}
	if err := db.WithContext(ctx).AutoMigrate(&TurnGuard{}); err != nil {
		return errors.Wrap(err, "auto migrate mcp memory tables")
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
