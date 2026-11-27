package files

import (
	"cmp"
	"context"
	"database/sql"
	"hash/fnv"
	"slices"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// LockProvider serializes mutations within a project scope.
type LockProvider interface {
	WithProjectLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash, project string, timeout time.Duration, fn func(tx *sql.Tx) error) error
}

// MultiProjectLockProvider serializes one mutation that spans several project
// scopes of the same caller, such as a write that resolves through the
// DefaultProject layer. Implementations must hold every scope for the whole
// callback transaction and acquire them in ProjectLockOrder, so two mutations
// over the same scopes cannot each hold one scope while waiting for the other.
type MultiProjectLockProvider interface {
	WithProjectsLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash string, projects []string, timeout time.Duration, fn func(tx *sql.Tx) error) error
}

// DefaultLockProvider implements advisory locks when available.
type DefaultLockProvider struct{}

// WithProjectLock acquires a scoped lock and executes the callback within a transaction.
func (p DefaultLockProvider) WithProjectLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash, project string, timeout time.Duration, fn func(tx *sql.Tx) error) error {
	return p.WithProjectsLock(ctx, db, isPostgres, apiKeyHash, []string{project}, timeout, fn)
}

// WithProjectsLock acquires every project scope in ProjectLockOrder inside one
// transaction and executes the callback while all of them are held. All scopes
// share one timeout: time spent waiting for one is not granted again to the next.
func (p DefaultLockProvider) WithProjectsLock(ctx context.Context, db *sql.DB, isPostgres bool, apiKeyHash string, projects []string, timeout time.Duration, fn func(tx *sql.Tx) error) error {
	ordered := ProjectLockOrder(apiKeyHash, projects...)
	if len(ordered) == 0 {
		return errors.New("at least one project scope is required")
	}
	if db == nil {
		return errors.New("db is required")
	}
	var options *sql.TxOptions
	if isPostgres {
		// Lock acquisition itself is a SELECT. A repeatable-read default could
		// pin a snapshot before the preceding lock holder commits its revision.
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	deadline := time.Now().Add(timeout)
	for _, project := range ordered {
		if err = acquireProjectLock(ctx, tx, isPostgres, apiKeyHash, project, deadline); err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				return errors.Wrap(rollbackErr, "rollback lock transaction")
			}
			return err
		}
	}

	if err = fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Wrap(rollbackErr, "rollback callback transaction")
		}
		return errors.WithStack(normalizeRevisionError(err))
	}

	if err = tx.Commit(); err != nil {
		return errors.Wrap(err, "commit transaction")
	}

	return nil
}

// acquireProjectLock obtains a project-scoped advisory lock within the
// transaction, retrying until deadline.
func acquireProjectLock(ctx context.Context, tx *sql.Tx, isPostgres bool, apiKeyHash, project string, deadline time.Time) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	if !isPostgres {
		return nil
	}

	key := hashLockKey(apiKeyHash, project)
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
		timer := time.NewTimer(min(50*time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.WithStack(ctx.Err())
		case <-timer.C:
		}
	}
}

// ProjectLockOrder returns the distinct non-empty projects in the global lock
// acquisition order: ascending advisory key, then name for equal keys. Every
// multi-project mutation must lock in this order to avoid lock-order inversion.
func ProjectLockOrder(apiKeyHash string, projects ...string) []string {
	ordered := make([]string, 0, len(projects))
	for _, project := range projects {
		if project != "" && !slices.Contains(ordered, project) {
			ordered = append(ordered, project)
		}
	}
	slices.SortFunc(ordered, func(a, b string) int {
		if c := cmp.Compare(hashLockKey(apiKeyHash, a), hashLockKey(apiKeyHash, b)); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return ordered
}

// hashLockKey derives a stable int64 key from apiKeyHash and project.
func hashLockKey(apiKeyHash, project string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(apiKeyHash))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(project))
	v := h.Sum64()
	return int64(v & 0x7FFFFFFFFFFFFFFF) //nolint:gosec // intentional truncation for advisory lock key
}
