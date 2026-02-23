package memory

import (
	"context"
	"database/sql"
	"hash/fnv"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/jackc/pgx/v5/stdlib"
)

// withSessionLock executes fn under a session-scoped lock.
func withSessionLock(ctx context.Context, db *sql.DB, apiKeyHash, project, sessionID string, timeout time.Duration, fn func(tx *sql.Tx) error) error {
	if db == nil {
		return errors.New("db is required")
	}
	if !isPostgresDB(db) {
		return fn(nil)
	}

	tx, beginErr := db.BeginTx(ctx, &sql.TxOptions{})
	if beginErr != nil {
		return errors.Wrap(beginErr, "begin transaction")
	}

	if err := acquireSessionLock(ctx, tx, apiKeyHash, project, sessionID, timeout); err != nil {
		_ = tx.Rollback()
		return err
	}

	if runErr := fn(tx); runErr != nil {
		_ = tx.Rollback()
		return runErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return errors.Wrap(commitErr, "commit transaction")
	}

	return nil
}

// acquireSessionLock attempts to acquire a session advisory lock in the current transaction.
func acquireSessionLock(ctx context.Context, tx *sql.Tx, apiKeyHash, project, sessionID string, timeout time.Duration) error {
	if tx == nil {
		return errors.New("transaction is required")
	}

	key := hashSessionLockKey(apiKeyHash, project, sessionID)
	deadline := time.Now().UTC().Add(timeout)
	for {
		var locked bool
		if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&locked); err != nil {
			return errors.Wrap(err, "acquire session advisory lock")
		}
		if locked {
			return nil
		}
		if time.Now().UTC().After(deadline) {
			return errors.WithStack(NewError(ErrCodeResourceBusy, "resource busy", true))
		}

		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "wait for session advisory lock")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// hashSessionLockKey derives a stable int64 lock key.
func hashSessionLockKey(apiKeyHash, project, sessionID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(apiKeyHash))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(project))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(sessionID))
	return int64(h.Sum64())
}

// isPostgresDB reports whether the active SQL driver is postgres/pgx.
func isPostgresDB(db *sql.DB) bool {
	if db == nil {
		return false
	}

	switch db.Driver().(type) {
	case *stdlib.Driver:
		return true
	default:
		return false
	}
}
