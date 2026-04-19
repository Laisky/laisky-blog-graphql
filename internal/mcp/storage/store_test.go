package storage

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFakeStorePutAndGet(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()

	payload := []byte("hello world")
	err := store.Put(ctx, "path/obj.png", bytes.NewReader(payload), int64(len(payload)), "image/png", map[string]string{"x-amz-meta-original-mime": "image/jpeg"})
	require.NoError(t, err)

	reader, size, err := store.Get(ctx, "path/obj.png")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.EqualValues(t, len(payload), size)

	meta, ok := store.ObjectMeta("path/obj.png")
	require.True(t, ok)
	require.Equal(t, "image/jpeg", meta["x-amz-meta-original-mime"])
}

func TestFakeStoreGetMissing(t *testing.T) {
	store := NewFakeStore()
	_, _, err := store.Get(context.Background(), "missing")
	require.Error(t, err)
}

func TestFakeStorePresignedGet(t *testing.T) {
	store := NewFakeStore()
	url, err := store.PresignedGet(context.Background(), "some/key.png", 30*time.Minute)
	require.NoError(t, err)
	require.Contains(t, url, "X-Amz-Expires=1800")
	require.Equal(t, 30*time.Minute, store.LastPresignTTL())
}

func TestFakeStoreEnsureLifecycleIdempotent(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	require.NoError(t, store.EnsureLifecycle(ctx, "mcp/", 7))
	require.NoError(t, store.EnsureLifecycle(ctx, "mcp/", 7))

	days, ok := store.Lifecycle("mcp/")
	require.True(t, ok)
	require.Equal(t, 7, days)
}

func TestMinIOClientRequiresConfig(t *testing.T) {
	_, err := NewMinIOClient(MinIOConfig{})
	require.Error(t, err)

	_, err = NewMinIOClient(MinIOConfig{Endpoint: "s3.example.com"})
	require.Error(t, err)

	_, err = NewMinIOClient(MinIOConfig{Endpoint: "s3.example.com", Bucket: "b"})
	require.Error(t, err)

	_, err = NewMinIOClient(MinIOConfig{Endpoint: "s3.example.com", Bucket: "b", AccessKey: "k"})
	require.Error(t, err)
}
