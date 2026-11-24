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

// TestDefaultLockProviderPostgresProjectsLockOrder verifies that a
// multi-project lock takes its scopes in ProjectLockOrder whatever the argument
// order: it may hold the earlier scope while waiting for the later one, but
// never the later scope while waiting for the earlier one.
func TestDefaultLockProviderPostgresProjectsLockOrder(t *testing.T) {
	db := openPostgresTestDB(t)
	provider := DefaultLockProvider{}
	ctx := context.Background()
	const tenant = "tenant-lock-order"
	ordered := ProjectLockOrder(tenant, "alpha", "beta")
	require.Len(t, ordered, 2)
	first, second := ordered[0], ordered[1]

	hold := func(project string) chan<- struct{} {
		entered, release := make(chan struct{}), make(chan struct{})
		go func() {
			_ = provider.WithProjectLock(ctx, db, true, tenant, project, 2*time.Second, func(*sql.Tx) error {
				close(entered)
				<-release
				return nil
			})
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("holder of %s did not enter its callback", project)
		}
		return release
	}
	// busy reports whether another transaction holds project. It never fails the
	// test itself because require.Eventually runs it on another goroutine.
	busy := func(project string) bool {
		err := provider.WithProjectLock(ctx, db, true, tenant, project, 50*time.Millisecond, func(*sql.Tx) error { return nil })
		return IsCode(err, ErrCodeResourceBusy)
	}
	lockBoth := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			// Reverse argument order: the provider must still lock in global order.
			done <- provider.WithProjectsLock(ctx, db, true, tenant, []string{second, first}, 5*time.Second, func(*sql.Tx) error { return nil })
		}()
		return done
	}

	release := hold(second)
	done := lockBoth()
	require.Eventually(t, func() bool { return busy(first) }, 2*time.Second, 20*time.Millisecond,
		"while waiting for the later scope the multi-lock holds the earlier one")
	close(release)
	require.NoError(t, <-done)

	release = hold(first)
	done = lockBoth()
	time.Sleep(200 * time.Millisecond)
	require.False(t, busy(second), "while waiting for the earlier scope the multi-lock must not hold the later one")
	close(release)
	require.NoError(t, <-done)
}
