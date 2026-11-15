package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// FakeStore is an in-memory ObjectStore used by unit tests. It records every
// Put and Delete call for assertion and serves reads out of RAM.
type FakeStore struct {
	mu         sync.Mutex
	objects    map[string]fakeObject
	lifecycles map[string]int
	puts       []string
	deletes    []string
	// lifecycleWrites counts EnsureLifecycle calls that wrote a rule.
	lifecycleWrites int
	// lifecycleCalls counts EnsureLifecycle and CheckLifecycle calls.
	lifecycleCalls int
	// presignTTL records the TTL the most recent PresignedGet observed, so tests
	// can assert on the requested lifetime.
	lastPresignTTL time.Duration
	// PutErr, GetErr, PresignErr, LifecycleErr, DeleteErr can be set by tests
	// to force failures. These fields and the hooks below may be assigned
	// directly before the store is shared; once other goroutines may call it,
	// change them through Inject.
	PutErr       error
	GetErr       error
	PresignErr   error
	LifecycleErr error
	DeleteErr    error
	// LifecycleCheckErr, when set, is returned by CheckLifecycle.
	LifecycleCheckErr error
	// BeforePut, when set, runs before an object is stored; a non-nil error
	// fails the Put without storing anything.
	BeforePut func(ctx context.Context, key string) error
	// AfterPut, when set, runs after an object is stored. A non-nil error is
	// returned from Put while the object stays stored, which models a client
	// that gave up (for example on a timeout) after the server committed it.
	AfterPut func(ctx context.Context, key string) error
	// BeforeDelete, when set, runs before an object is deleted; a non-nil
	// error fails the Delete without removing anything.
	BeforeDelete func(ctx context.Context, key string) error
	// HostBase is the synthetic base URL used when building presigned URLs.
	HostBase string
	// DiscardBodies, when set, stores every object with an empty body (the
	// key, content type and metadata are kept), so memory measurements see
	// only what the caller holds.
	DiscardBodies bool
}

type fakeObject struct {
	Body        []byte
	ContentType string
	UserMeta    map[string]string
}

// fakeInjections is a consistent snapshot of a FakeStore's injected failures
// and hooks, read under f.mu.
type fakeInjections struct {
	putErr, getErr, presignErr, lifecycleErr, deleteErr, lifecycleCheckErr error
	beforePut, afterPut, beforeDelete                                      func(ctx context.Context, key string) error
	hostBase                                                               string
	discardBodies                                                          bool
}

// Inject runs change with the store locked, so a test can change injected
// failures and hooks while other goroutines (a lifecycle controller,
// parallel uploads) are calling the store. change must not call f.
func (f *FakeStore) Inject(change func(*FakeStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// injected snapshots the injected failures and hooks under f.mu. Hooks run
// after the lock is released, so they may block or call the store.
func (f *FakeStore) injected() fakeInjections {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeInjections{
		putErr: f.PutErr, getErr: f.GetErr, presignErr: f.PresignErr,
		lifecycleErr: f.LifecycleErr, deleteErr: f.DeleteErr, lifecycleCheckErr: f.LifecycleCheckErr,
		beforePut: f.BeforePut, afterPut: f.AfterPut, beforeDelete: f.BeforeDelete,
		hostBase: f.HostBase, discardBodies: f.DiscardBodies,
	}
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
	inj := f.injected()
	if inj.putErr != nil {
		return inj.putErr
	}
	if inj.beforePut != nil {
		if err := inj.beforePut(ctx, key); err != nil {
			return err
		}
	}
	buf := new(bytes.Buffer)
	var sink io.Writer = buf
	if inj.discardBodies {
		sink = io.Discard
	}
	if _, err := io.Copy(sink, body); err != nil {
		return errors.Wrap(err, "fake put read body")
	}
	metaCopy := make(map[string]string, len(userMeta))
	for k, v := range userMeta {
		metaCopy[k] = v
	}
	f.mu.Lock()
	f.objects[key] = fakeObject{
		Body:        buf.Bytes(),
		ContentType: contentType,
		UserMeta:    metaCopy,
	}
	f.puts = append(f.puts, key)
	f.mu.Unlock()
	_ = size
	if inj.afterPut != nil {
		return inj.afterPut(ctx, key)
	}
	return nil
}

// Delete removes an object; deleting a missing key succeeds, like S3.
func (f *FakeStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "fake delete context canceled")
	}
	inj := f.injected()
	if inj.deleteErr != nil {
		return inj.deleteErr
	}
	if inj.beforeDelete != nil {
		if err := inj.beforeDelete(ctx, key); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deletes = append(f.deletes, key)
	return nil
}

// sortedKeysLocked returns the stored keys in lexical order; callers must hold f.mu.
func (f *FakeStore) sortedKeysLocked() []string {
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Keys returns the keys of every stored object in lexical order.
func (f *FakeStore) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sortedKeysLocked()
}

// PutKeys returns the keys of every successful Put in call order.
func (f *FakeStore) PutKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.puts...)
}

// DeletedKeys returns the keys of every successful Delete in call order.
func (f *FakeStore) DeletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletes...)
}

// Get returns a reader for an object that was previously stored.
func (f *FakeStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, errors.Wrap(err, "fake get context canceled")
	}
	if err := f.injected().getErr; err != nil {
		return nil, 0, err
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
	inj := f.injected()
	if inj.presignErr != nil {
		return "", inj.presignErr
	}
	f.mu.Lock()
	f.lastPresignTTL = ttl
	f.mu.Unlock()
	return fmt.Sprintf("%s/%s?X-Amz-Expires=%d&X-Amz-Signature=fake", inj.hostBase, key, int(ttl.Seconds())), nil
}

// EnsureLifecycle records the requested expiry so tests can assert on it. It
// writes (and reports so) only when no rule with that expiry is recorded.
func (f *FakeStore) EnsureLifecycle(ctx context.Context, prefix string, days int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, errors.Wrap(err, "fake lifecycle context canceled")
	}
	f.mu.Lock()
	f.lifecycleCalls++
	lifecycleErr := f.LifecycleErr
	f.mu.Unlock()
	if lifecycleErr != nil {
		return false, lifecycleErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, ok := f.lifecycles[prefix]; ok && got == days {
		return false, nil
	}
	f.lifecycles[prefix] = days
	f.lifecycleWrites++
	return true, nil
}

// CheckLifecycle reports whether a rule for prefix expiring within days was
// recorded. LifecycleCheckErr, when set, is returned instead.
func (f *FakeStore) CheckLifecycle(ctx context.Context, prefix string, days int) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, "fake lifecycle context canceled")
	}
	f.mu.Lock()
	f.lifecycleCalls++
	checkErr := f.LifecycleCheckErr
	f.mu.Unlock()
	if checkErr != nil {
		return checkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, ok := f.lifecycles[prefix]; !ok || got > days {
		return errors.Wrapf(ErrLifecycleRuleMissing, "fake: no rule for %q", prefix)
	}
	return nil
}

// RemoveLifecycle drops the rule recorded for prefix, as another writer
// replacing the bucket configuration would.
func (f *FakeStore) RemoveLifecycle(prefix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.lifecycles, prefix)
}

// LifecycleCalls reports how many EnsureLifecycle and CheckLifecycle calls ran.
func (f *FakeStore) LifecycleCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lifecycleCalls
}

// LifecycleWrites reports how many EnsureLifecycle calls wrote a rule.
func (f *FakeStore) LifecycleWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lifecycleWrites
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
