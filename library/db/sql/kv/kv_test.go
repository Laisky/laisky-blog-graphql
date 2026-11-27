package kv

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

// testClock is a movable clock shared by a store and its test.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_")))
	require.NoError(t, err, "failed to connect to in-memory db")
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	return db
}

func setupTestKv(t *testing.T, opts ...Option) (*Kv, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	// Create a new Kv instance with the test table name.
	kvInstance, err := NewKv(context.Background(), db, append([]Option{WithDBName("test_kv")}, opts...)...)
	require.NoError(t, err, "failed to create kv instance")
	return kvInstance, db
}

func TestSetAndGet(t *testing.T) {
	kvInstance, _ := setupTestKv(t)
	ctx := context.Background()

	key, value := "testkey", "testvalue"
	ttl := 5 * time.Second

	err := kvInstance.SetWithTTL(ctx, key, value, ttl)
	require.NoError(t, err, "SetWithTTL should not error")

	item, err := kvInstance.Get(ctx, key)
	require.NoError(t, err, "Get should not error")
	require.Equal(t, key, item.Key)
	require.Equal(t, value, item.Value)
}

func TestKeyExpiration(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)}
	kvInstance, _ := setupTestKv(t, WithClock(clock.Now))
	ctx := context.Background()

	key, value := "expirekey", "expirevalue"
	require.NoError(t, kvInstance.SetWithTTL(ctx, key, value, time.Second), "SetWithTTL should not error")
	_, err := kvInstance.Get(ctx, key)
	require.NoError(t, err, "key is live before its TTL")

	clock.Advance(2 * time.Second)
	_, err = kvInstance.Get(ctx, key)
	require.ErrorIs(t, err, ErrKeyExpired, "key should be expired")
	_, err = kvInstance.Get(ctx, key)
	require.ErrorIs(t, err, ErrKeyNotFound, "an expired key is deleted when read")
}

func TestExistsAndDel(t *testing.T) {
	kvInstance, _ := setupTestKv(t)
	ctx := context.Background()

	key, value := "existkey", "existvalue"
	err := kvInstance.SetWithExpireAt(ctx, key, value, time.Now().Add(10*time.Second))
	require.NoError(t, err, "SetWithExpireAt should not error")

	exists, err := kvInstance.Exists(ctx, key)
	require.NoError(t, err, "Exists should not error")
	require.True(t, exists, "key should exist")

	err = kvInstance.Del(ctx, key)
	require.NoError(t, err, "Del should not error")

	exists, err = kvInstance.Exists(ctx, key)
	require.NoError(t, err, "Exists after deletion should not error")
	require.False(t, exists, "key should not exist after deletion")
}

// TestWithTxCommitsAndRollsBackWithTheTransaction verifies a tx-bound store
// writes nothing visible unless the caller's transaction commits.
func TestWithTxCommitsAndRollsBackWithTheTransaction(t *testing.T) {
	kvInstance, db := setupTestKv(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, kvInstance.WithTx(tx).SetWithTTL(ctx, "rolled_back", "v", time.Minute))
	require.NoError(t, tx.Rollback())
	_, err = kvInstance.Get(ctx, "rolled_back")
	require.ErrorIs(t, err, ErrKeyNotFound)

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, kvInstance.WithTx(tx).SetWithTTL(ctx, "committed", "v", time.Minute))
	require.NoError(t, tx.Commit())
	item, err := kvInstance.Get(ctx, "committed")
	require.NoError(t, err)
	require.Equal(t, "v", item.Value)
}

// TestDeleteExpiredIsBounded verifies a sweep removes only expired items and
// at most limit of them.
func TestDeleteExpiredIsBounded(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)}
	kvInstance, _ := setupTestKv(t, WithClock(clock.Now))
	ctx := context.Background()
	for i := range 5 {
		require.NoError(t, kvInstance.SetWithTTL(ctx, fmt.Sprintf("short_%d", i), "v", time.Second))
	}
	require.NoError(t, kvInstance.SetWithTTL(ctx, "long", "v", time.Hour))
	clock.Advance(time.Minute)

	removed, err := kvInstance.DeleteExpired(ctx, 3)
	require.NoError(t, err)
	require.EqualValues(t, 3, removed)
	removed, err = kvInstance.DeleteExpired(ctx, 3)
	require.NoError(t, err)
	require.EqualValues(t, 2, removed)
	removed, err = kvInstance.DeleteExpired(ctx, 3)
	require.NoError(t, err)
	require.Zero(t, removed)
	_, err = kvInstance.Get(ctx, "long")
	require.NoError(t, err, "a live item survives the sweep")

	_, err = kvInstance.DeleteExpired(ctx, 0)
	require.Error(t, err)
}

// TestOpenRunsNoDDL verifies Open uses a table created elsewhere and fails
// cleanly when it is missing.
func TestOpenRunsNoDDL(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	store, err := Open(db, WithDBName("open_kv"))
	require.NoError(t, err)
	require.Error(t, store.SetWithTTL(ctx, "k", "v", time.Minute), "Open must not create the table")

	require.NoError(t, CreateTable(ctx, db, WithDBName("open_kv")))
	require.NoError(t, CreateTable(ctx, db, WithDBName("open_kv")), "CreateTable is idempotent")
	require.NoError(t, store.SetWithTTL(ctx, "k", "v", time.Minute))
}

// TestKeyAndTTLValidation verifies the accepted key alphabet and length and
// the TTL bounds.
func TestKeyAndTTLValidation(t *testing.T) {
	kvInstance, _ := setupTestKv(t)
	ctx := context.Background()
	require.NoError(t, kvInstance.SetWithTTL(ctx, strings.Repeat("a", 128), "v", time.Minute))
	require.Error(t, kvInstance.SetWithTTL(ctx, strings.Repeat("a", 129), "v", time.Minute))
	require.Error(t, kvInstance.SetWithTTL(ctx, "has:colon", "v", time.Minute))
	require.Error(t, kvInstance.SetWithTTL(ctx, "k", "v", 0))
	require.Error(t, kvInstance.SetWithTTL(ctx, "k", "v", maxTTL+time.Second))
	require.Error(t, kvInstance.SetWithExpireAt(ctx, "k", "v", time.Now().Add(-time.Second)))
	require.Error(t, kvInstance.SetWithExpireAt(ctx, "has:colon", "v", time.Now().Add(time.Minute)))
}
