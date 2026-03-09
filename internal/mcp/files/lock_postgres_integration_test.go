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

// TestDefaultLockProviderPostgresTimeout verifies a held PostgreSQL advisory lock times out competing writers.
func TestDefaultLockProviderPostgresTimeout(t *testing.T) {
	dbA := openPostgresTestDB(t)
	dbB := openPostgresTestDB(t)
	provider := DefaultLockProvider{}

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- provider.WithProjectLock(context.Background(), dbA, true, "tenant-a", "proj", 2*time.Second, func(_ *sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first lock holder did not enter callback")
	}

	err := provider.WithProjectLock(context.Background(), dbB, true, "tenant-a", "proj", 120*time.Millisecond, func(_ *sql.Tx) error {
		return nil
	})
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeResourceBusy))

	close(release)
	require.NoError(t, <-done)
}
