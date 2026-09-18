package oneapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newPoolTestDB opens a private in-memory SQLite database through the exact
// production constructor, so the pool size and the prepared-statement setting
// under test are the shipped ones.
func newPoolTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := NewDB(t.Context(), Options{Driver: "sqlite", SQLitePath: path})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &oneAPIToken{}, &oneAPIOption{}, &PasskeyCredential{}, &SSOUserLink{}))
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

// TestSingleConnectionPoolDoesNotDeadlockOnNewStatements reproduces a hard
// deadlock between two resources:
//
//   - GORM's prepared-statement cache mutex, which the LRU statement store
//     holds across database/sql's PrepareContext call, and
//   - the single pooled SQLite connection, which PrepareContext must acquire.
//
// A goroutine inside a transaction owns the only connection and then needs the
// statement-cache mutex to prepare its next statement, while the goroutine
// holding that mutex waits for a connection that will never be released. The
// process then hangs forever; the full `go test -race -cover ./...` run first
// surfaced it as a 10-minute test timeout.
//
// Each case uses a fresh database so no statement is pre-cached, which is the
// window where the cycle is reachable. The bounded deadline is the assertion:
// the deadlock has no recovery path, so a hang is the failure.
func TestSingleConnectionPoolDoesNotDeadlockOnNewStatements(t *testing.T) {
	const (
		workers  = 24
		deadline = 30 * time.Second
	)

	db := newPoolTestDB(t)
	for i := range workers {
		require.NoError(t, db.Create(&User{
			UUID: gutils.UUID7(), Username: fmt.Sprintf("pool-%d", i),
			DisplayName: "Pool", Email: fmt.Sprintf("pool-%d@example.com", i),
			Status: StatusEnabled, Role: RoleCommonUser,
			AccessToken: strings.ReplaceAll(gutils.UUID7(), "-", ""), AffCode: fmt.Sprintf("P%03d", i),
		}).Error)
	}

	done := make(chan struct{})
	var wait sync.WaitGroup
	for i := range workers {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			ctx := context.Background()
			// Half the workers hold a transaction while issuing a query whose
			// statement is not cached yet; the other half prepare distinct new
			// statements outside a transaction. That is the exact interleaving
			// that closes the mutex/connection cycle.
			if worker%2 == 0 {
				_ = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var user User
					_ = tx.Where("username = ?", fmt.Sprintf("pool-%d", worker)).First(&user).Error
					var links []SSOUserLink
					_ = tx.Where("oneapi_user_id = ?", user.ID).Find(&links).Error
					return nil
				})
				return
			}
			var users []User
			_ = db.WithContext(ctx).Where("status = ? AND role = ? AND aff_code <> ?",
				StatusEnabled, RoleCommonUser, fmt.Sprintf("P%03d", worker)).Find(&users).Error
			var tokens []oneAPIToken
			_ = db.WithContext(ctx).Where("user_id = ? AND name <> ?", worker,
				fmt.Sprintf("token-%d", worker)).Find(&tokens).Error
		}(i)
	}
	go func() {
		wait.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(deadline):
		require.FailNow(t, "deadlock",
			"concurrent statement preparation deadlocked against the single pooled connection")
	}
}

// TestPoolSizeFor covers the bounds resolution that decides both the pool and
// the prepared-statement cache, including the rejection of an impossible pool.
func TestPoolSizeFor(t *testing.T) {
	t.Parallel()

	t.Run("sqlite is always single-connection", func(t *testing.T) {
		t.Parallel()
		idle, open, err := poolSizeFor(true, Options{MaxIdleConns: 9, MaxOpenConns: 99})
		require.NoError(t, err)
		require.Equal(t, 1, idle)
		require.Equal(t, 1, open)
		require.Less(t, open, minPrepareStmtPoolSize,
			"a single connection must keep the statement cache disabled")
	})

	t.Run("network drivers keep a pool large enough for the statement cache", func(t *testing.T) {
		t.Parallel()
		idle, open, err := poolSizeFor(false, Options{})
		require.NoError(t, err)
		require.Equal(t, defaultMaxIdleConns, idle)
		require.Equal(t, defaultMaxOpenConns, open)
		require.GreaterOrEqual(t, open, minPrepareStmtPoolSize)
	})

	t.Run("explicit bounds are honored", func(t *testing.T) {
		t.Parallel()
		idle, open, err := poolSizeFor(false, Options{MaxIdleConns: 2, MaxOpenConns: 7})
		require.NoError(t, err)
		require.Equal(t, 2, idle)
		require.Equal(t, 7, open)
	})

	t.Run("an idle bound above the open bound is rejected", func(t *testing.T) {
		t.Parallel()
		_, _, err := poolSizeFor(false, Options{MaxIdleConns: 9, MaxOpenConns: 3})
		require.Error(t, err)
	})

	t.Run("a hand-lowered single-connection pool also disables the cache", func(t *testing.T) {
		t.Parallel()
		_, open, err := poolSizeFor(false, Options{MaxIdleConns: 1, MaxOpenConns: 1})
		require.NoError(t, err)
		require.Less(t, open, minPrepareStmtPoolSize,
			"one connection deadlocks the statement cache on every driver, not only sqlite")
	})
}

// TestSQLitePoolDisablesPreparedStatementCache pins the configuration that
// makes the deadlock unreachable. With one connection, GORM's statement cache
// can never make progress while that connection is busy, so the cache must be
// off for SQLite. The multi-connection drivers keep it.
func TestSQLitePoolDisablesPreparedStatementCache(t *testing.T) {
	db := newPoolTestDB(t)
	require.False(t, db.Config.PrepareStmt,
		"a single-connection pool must not also cache prepared statements")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)
}
