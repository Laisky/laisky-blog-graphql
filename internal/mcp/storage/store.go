// Package storage defines the object storage abstraction used by MCP image support.
// The interface intentionally covers only the operations the image pipeline needs:
// idempotent uploads, per-key reads and deletes, prefix listing for orphan
// reconciliation, presigned downloads, and bucket-level lifecycle installation.
// Production wires MinIO via storage.NewMinIOClient; tests use the in-memory
// FakeStore provided in this package.
package storage

import (
	"context"
	"io"
	"time"
)

// ObjectInfo describes one stored object returned by ObjectStore.List.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ObjectStore is the contract image processing and MCP tool code depend on.
// Implementations must be safe for concurrent use.
type ObjectStore interface {
	// Put uploads an object keyed by key with the provided content type and user metadata.
	// Re-uploading identical content under the same key MUST be idempotent.
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string, userMeta map[string]string) error

	// Get streams an object back. The returned ReadCloser must be closed by the caller.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)

	// Delete removes an object. Deleting a key that does not exist MUST succeed,
	// so compensation and reconciliation can retry it safely.
	Delete(ctx context.Context, key string) error

	// List returns at most limit objects whose keys start with prefix and sort
	// strictly after startAfter, in ascending key order. An empty result means
	// the listing is exhausted.
	List(ctx context.Context, prefix, startAfter string, limit int) ([]ObjectInfo, error)

	// PresignedGet returns a short-lived URL that third parties can use to fetch the object.
	PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error)

	// EnsureLifecycle installs a bucket-level expiry rule restricted to a prefix.
	// The call must be idempotent; repeated invocations MUST NOT create duplicate rules.
	EnsureLifecycle(ctx context.Context, prefix string, days int) error
}
