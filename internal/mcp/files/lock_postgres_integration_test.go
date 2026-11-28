package files

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// openPostgresTestDB opens a PostgreSQL database for advisory-lock integration tests.
func openPostgresTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("MCP_FILES_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MCP_FILES_TEST_POSTGRES_DSN is not set")
	}

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(context.Background()))
	return db
}

// TestDefaultLockProviderPostgresAdmission verifies actual database-scoped
// contention, independence across tenant keys and release on rollback.
func TestDefaultLockProviderPostgresAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dbA, dbB := openPostgresTestDB(t), openPostgresTestDB(t)
	tx, err := dbA.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Hold the same key on an independent connection, as another replica can.
	_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", tenantLockKey(t.Name()))
	require.NoError(t, err)
	lock := DefaultLockProvider{}
	calls := 0
	fn := func(*sql.Tx) error { calls++; return nil }
	err = lock.WithTenantLock(ctx, dbB, true, t.Name(), fn)
	require.True(t, IsCode(err, ErrCodeResourceBusy), "%v", err)
	require.Zero(t, calls)
	require.NoError(t, lock.WithTenantLock(ctx, dbB, true, t.Name()+"-other", fn))
	require.NoError(t, tx.Rollback())
	require.NoError(t, lock.WithTenantLock(ctx, dbB, true, t.Name(), fn))
	require.Equal(t, 2, calls)
}
