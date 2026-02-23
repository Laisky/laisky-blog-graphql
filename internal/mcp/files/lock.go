package files

import (
	"context"
	"database/sql"
	"hash/fnv"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// LockProvider serializes mutations within a project scope.
type LockProvider interface {
	WithProjectLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash, project string, timeout time.Duration, fn func(tx *sql.Tx) error) error
}

// DefaultLockProvider implements advisory locks when available.
type DefaultLockProvider struct{}

// WithProjectLock acquires a scoped lock and executes the callback within a transaction.
func (p DefaultLockProvider) WithProjectLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash, project string, timeout time.Duration, fn func(tx *sql.Tx) error) error {
	if db == nil {
		return errors.New("db is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	if err = acquireProjectLock(ctx, tx, isPostgres, apiKeyHash, project, timeout); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Wrap(rollbackErr, "rollback lock transaction")
		}
		return err
	}

	if err = fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Wrap(rollbackErr, "rollback callback transaction")
		}
		return err
	}

	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "commit transaction")
	}

	return nil
}

// acquireProjectLock obtains a project-scoped advisory lock within the transaction.
func acquireProjectLock(ctx context.Context, tx *sql.Tx, isPostgres bool, apiKeyHash, project string, timeout time.Duration) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	if !isPostgres {
		return nil
	}

	key := hashLockKey(apiKeyHash, project)
	deadline := time.Now().Add(timeout)
	for {
		var locked bool
		if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&locked); err != nil {
			return errors.Wrap(err, "acquire advisory lock")
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return NewError(ErrCodeResourceBusy, "resource busy", true)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hashLockKey derives a stable int64 key from apiKeyHash and project.
func hashLockKey(apiKeyHash, project string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(apiKeyHash))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(project))
	return int64(h.Sum64())
}
