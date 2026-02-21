package memory

import (
	"context"
	"hash/fnv"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/gorm"
)

// withSessionLock executes fn under a session-scoped lock.
func withSessionLock(ctx context.Context, db *gorm.DB, apiKeyHash, project, sessionID string, timeout time.Duration, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("db is required")
	}
	if !isPostgresDialect(db) {
		return fn(db.WithContext(ctx))
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireSessionLock(ctx, tx, apiKeyHash, project, sessionID, timeout); err != nil {
			return err
		}
		return fn(tx)
	})
}

// acquireSessionLock attempts to acquire a session advisory lock in the current transaction.
func acquireSessionLock(ctx context.Context, tx *gorm.DB, apiKeyHash, project, sessionID string, timeout time.Duration) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	if !isPostgresDialect(tx) {
		return nil
	}

	key := hashSessionLockKey(apiKeyHash, project, sessionID)
	deadline := time.Now().Add(timeout)
	for {
		var locked bool
		if err := tx.WithContext(ctx).Raw("SELECT pg_try_advisory_xact_lock(?)", key).Scan(&locked).Error; err != nil {
			return errors.Wrap(err, "acquire session advisory lock")
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.WithStack(NewError(ErrCodeResourceBusy, "resource busy", true))
		}
		time.Sleep(50 * time.Millisecond)
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

// isPostgresDialect reports whether the active gorm dialector is postgres.
func isPostgresDialect(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if db.Dialector == nil {
		return false
	}
	return db.Dialector.Name() == "postgres"
}
