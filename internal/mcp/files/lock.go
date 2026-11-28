package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"

	errors "github.com/Laisky/errors/v2"
)

// LockProvider serializes file mutations for one authenticated tenant.
type LockProvider interface {
	WithTenantLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash string, fn func(tx *sql.Tx) error) error
}

// DefaultLockProvider implements the database-backed mutation transaction.
type DefaultLockProvider struct{}

// WithTenantLock executes fn at most once after acquiring database write admission.
func (DefaultLockProvider) WithTenantLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash string, fn func(tx *sql.Tx) error) error {
	if db == nil {
		return errors.New("db is required")
	}
	if !isPostgres {
		return withSQLiteWriteIntent(ctx, db, fn)
	}
	// Advisory admission itself is a SELECT. READ COMMITTED prevents that
	// statement from pinning a stale snapshot before the preceding commit.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errors.Wrap(err, "begin mutation transaction")
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", tenantLockKey(apiKeyHash)).Scan(&locked); err != nil {
		return errors.Wrap(err, "acquire tenant advisory lock")
	}
	if !locked {
		return NewError(ErrCodeResourceBusy, "resource busy", true)
	}
	return finishMutation(tx, fn)
}

// finishMutation runs the callback and commits; no post-admission failure is
// converted into retryable contention. The caller always defers rollback.
func finishMutation(tx *sql.Tx, fn func(tx *sql.Tx) error) error {
	if err := fn(tx); err != nil {
		return errors.WithStack(normalizeRevisionError(err))
	}
	return errors.Wrap(tx.Commit(), "commit mutation transaction")
}

// withTenantLock routes all namespace writers through the same transaction.
func (s *Service) withTenantLock(ctx context.Context, apiKeyHash string, fn func(tx *sql.Tx) error) error {
	return s.lockProvider.WithTenantLock(ctx, s.db, s.isPostgres, apiKeyHash, fn)
}

// tenantLockKey is namespaced outside the former project-key derivation.
func tenantLockKey(apiKeyHash string) int64 {
	sum := sha256.Sum256([]byte("mcp-fileio-tenant-mutation:v1\x00" + apiKeyHash))
	return int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // advisory keys intentionally use all signed int64 bits
}
