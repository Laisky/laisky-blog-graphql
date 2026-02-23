package kv

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func setupTestKv(t *testing.T) *Kv {
	db, err := sql.Open("sqlite3", "file::memory:?cache=shared")
	require.NoError(t, err, "failed to connect to in-memory db")
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	// Create a new Kv instance with the test table name.
	kvInstance, err := NewKv(db, WithDBName("test_kv"))
	require.NoError(t, err, "failed to create kv instance")
	return kvInstance
}

func TestSetAndGet(t *testing.T) {
	kvInstance := setupTestKv(t)
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
	kvInstance := setupTestKv(t)
	ctx := context.Background()

	key, value := "expirekey", "expirevalue"
	ttl := 1 * time.Second

	err := kvInstance.SetWithTTL(ctx, key, value, ttl)
	require.NoError(t, err, "SetWithTTL should not error")

	// Wait for the key to expire.
	time.Sleep(2 * time.Second)
	_, err = kvInstance.Get(ctx, key)
	require.Error(t, err, "key should be expired")
}

func TestExistsAndDel(t *testing.T) {
	kvInstance := setupTestKv(t)
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
