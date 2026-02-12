package files

import (
	"context"
	"hash/fnv"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/gorm"
)

// LockProvider serializes mutations within a project scope.
type LockProvider interface {
	WithProjectLock(ctx context.Context, db *gorm.DB, apiKeyHash, project string, timeout time.Duration, fn func(tx *gorm.DB) error) error
}

// DefaultLockProvider implements advisory locks when available.
type DefaultLockProvider struct{}

// WithProjectLock acquires a scoped lock and executes the callback within a transaction.
func (p DefaultLockProvider) WithProjectLock(ctx context.Context, db *gorm.DB, apiKeyHash, project string, timeout time.Duration, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("db is required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireProjectLock(ctx, tx, apiKeyHash, project, timeout); err != nil {
			return err
		}
		return fn(tx)
	})
}

// acquireProjectLock obtains a project-scoped advisory lock within the transaction.
func acquireProjectLock(ctx context.Context, tx *gorm.DB, apiKeyHash, project string, timeout time.Duration) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	if !isPostgresDialect(tx) {
		return nil
	}

	key := hashLockKey(apiKeyHash, project)
	deadline := time.Now().Add(timeout)
	for {
		var locked bool
		if err := tx.WithContext(ctx).Raw("SELECT pg_try_advisory_xact_lock(?)", key).Scan(&locked).Error; err != nil {
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
