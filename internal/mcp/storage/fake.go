package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// FakeStore is an in-memory ObjectStore used by unit tests. It records every
// Put call for assertion and serves reads out of RAM.
type FakeStore struct {
	mu         sync.Mutex
	objects    map[string]fakeObject
	lifecycles map[string]int
	// presignTTL records the TTL the most recent PresignedGet observed, so tests
	// can assert on the requested lifetime.
	lastPresignTTL time.Duration
	// PutErr, GetErr, PresignErr, LifecycleErr can be set by tests to force failures.
	PutErr       error
	GetErr       error
	PresignErr   error
	LifecycleErr error
	// HostBase is the synthetic base URL used when building presigned URLs.
	HostBase string
}

type fakeObject struct {
	Body        []byte
	ContentType string
	UserMeta    map[string]string
}

// NewFakeStore constructs an empty FakeStore with a default host base suitable for tests.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		objects:    make(map[string]fakeObject),
		lifecycles: make(map[string]int),
		HostBase:   "https://fake-s3.local/bucket",
	}
}

// Put stores the object in memory.
func (f *FakeStore) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string, userMeta map[string]string) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "fake put context canceled")
	}
	if f.PutErr != nil {
		return f.PutErr
	}
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, body); err != nil {
		return errors.Wrap(err, "fake put read body")
	}
	metaCopy := make(map[string]string, len(userMeta))
	for k, v := range userMeta {
		metaCopy[k] = v
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{
		Body:        buf.Bytes(),
		ContentType: contentType,
		UserMeta:    metaCopy,
	}
	_ = size
	return nil
}

// Get returns a reader for an object that was previously stored.
func (f *FakeStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, errors.Wrap(err, "fake get context canceled")
	}
	if f.GetErr != nil {
		return nil, 0, f.GetErr
	}
	f.mu.Lock()
	obj, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, 0, errors.Errorf("fake store: object not found for key %q", key)
	}
	return io.NopCloser(bytes.NewReader(obj.Body)), int64(len(obj.Body)), nil
}

// PresignedGet returns a synthetic URL. Tests can inspect LastPresignTTL to verify the TTL.
func (f *FakeStore) PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.Wrap(err, "fake presign context canceled")
	}
	if f.PresignErr != nil {
		return "", f.PresignErr
	}
	f.mu.Lock()
	f.lastPresignTTL = ttl
	f.mu.Unlock()
	return fmt.Sprintf("%s/%s?X-Amz-Expires=%d&X-Amz-Signature=fake", f.HostBase, key, int(ttl.Seconds())), nil
}

// EnsureLifecycle records the requested expiry so tests can assert on it.
func (f *FakeStore) EnsureLifecycle(ctx context.Context, prefix string, days int) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "fake lifecycle context canceled")
	}
	if f.LifecycleErr != nil {
		return f.LifecycleErr
	}
	f.mu.Lock()
	f.lifecycles[prefix] = days
	f.mu.Unlock()
	return nil
}

// LastPresignTTL reports the TTL used by the most recent PresignedGet call.
func (f *FakeStore) LastPresignTTL() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPresignTTL
}

// ObjectBytes returns a snapshot of the stored object; used for assertions.
func (f *FakeStore) ObjectBytes(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, false
	}
	out := make([]byte, len(obj.Body))
	copy(out, obj.Body)
	return out, true
}

// ObjectMeta returns a snapshot of the stored user metadata for a key.
func (f *FakeStore) ObjectMeta(key string) (map[string]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(obj.UserMeta))
	for k, v := range obj.UserMeta {
		out[k] = v
	}
	return out, true
}

// Lifecycle returns the recorded expiry in days for a prefix, if any.
func (f *FakeStore) Lifecycle(prefix string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	days, ok := f.lifecycles[prefix]
	return days, ok
}

// ObjectCount returns the number of stored objects.
func (f *FakeStore) ObjectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}
