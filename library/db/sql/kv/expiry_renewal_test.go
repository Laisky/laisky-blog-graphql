package kv

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// renewBeforeDelete interposes a renewal after the stale read but before SQL
// attempts to delete. The renewal and delete run on distinct connections.
type renewBeforeDelete struct {
	Executor
	before func()
}

func (e *renewBeforeDelete) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(query, "DELETE FROM ") {
		e.before()
	}
	return e.Executor.ExecContext(ctx, query, args...)
}

func TestLazyExpiryPreservesRenewalSQLite(t *testing.T) {
	checkExpiryRenewal(t, setupTestDB(t))
}

func TestLazyExpiryPreservesRenewalPostgres(t *testing.T) {
	dsn := os.Getenv("FILEIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("FILEIO_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	checkExpiryRenewal(t, db)
}

func checkExpiryRenewal(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	name := "expiry_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	clock := &testClock{now: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	opts := []Option{WithDBName(name), WithClock(clock.Now)}
	require.NoError(t, CreateTable(ctx, db, opts...))
	t.Cleanup(func() {
		// The identifier is generated locally and validated by CreateTable.
		_, err := db.ExecContext(context.Background(), "DROP TABLE "+name) //nolint:gosec // test-owned random identifier
		require.NoError(t, err)
	})
	readerConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, readerConn.Close()) }()
	writerConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, writerConn.Close()) }()
	writer, err := Open(writerConn, opts...)
	require.NoError(t, err)

	require.NoError(t, writer.SetWithTTL(ctx, "key", "expired", time.Second))
	clock.Advance(2 * time.Second)
	wantExpires := clock.Now().Add(time.Minute)
	hook := &renewBeforeDelete{Executor: readerConn, before: func() {
		require.NoError(t, writer.SetWithExpireAt(ctx, "key", "renewed", wantExpires))
	}}
	reader, err := Open(hook, opts...)
	require.NoError(t, err)
	_, err = reader.Get(ctx, "key")
	require.ErrorIs(t, err, ErrKeyExpired)
	item, err := writer.Get(ctx, "key")
	require.NoError(t, err)
	require.Equal(t, "renewed", item.Value)
	require.True(t, wantExpires.Equal(item.ExpireAt))

	// The sweep uses the same inclusive cutoff and preserves a renewal.
	clock.Advance(time.Minute)
	wantExpires = clock.Now().Add(time.Minute)
	n, err := reader.DeleteExpired(ctx, 4)
	require.NoError(t, err)
	require.Zero(t, n)
	item, err = writer.Get(ctx, "key")
	require.NoError(t, err)
	require.True(t, wantExpires.Equal(item.ExpireAt))
	clock.Advance(time.Minute)
	n, err = writer.DeleteExpired(ctx, 4)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	_, err = writer.Get(ctx, "key")
	require.ErrorIs(t, err, ErrKeyNotFound)
}
