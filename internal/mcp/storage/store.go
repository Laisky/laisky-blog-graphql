// Package storage defines the object storage abstraction used by MCP image support.
// The interface intentionally covers only the operations the image pipeline needs:
// idempotent uploads, per-key reads, presigned downloads, and bucket-level lifecycle
// installation. Production wires MinIO via storage.NewMinIOClient; tests use the
// in-memory FakeStore provided in this package.
package storage

import (
	"context"
	"io"
	"time"
)

// ObjectStore is the contract image processing and MCP tool code depend on.
// Implementations must be safe for concurrent use.
type ObjectStore interface {
	// Put uploads an object keyed by key with the provided content type and user metadata.
	// Re-uploading identical content under the same key MUST be idempotent.
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string, userMeta map[string]string) error

	// Get streams an object back. The returned ReadCloser must be closed by the caller.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)

	// PresignedGet returns a short-lived URL that third parties can use to fetch the object.
	PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error)

	// EnsureLifecycle installs a bucket-level expiry rule restricted to a prefix.
	// The call must be idempotent; repeated invocations MUST NOT create duplicate rules.
	EnsureLifecycle(ctx context.Context, prefix string, days int) error
}
