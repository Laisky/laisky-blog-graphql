package files

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// keyedMutexLockProvider serializes callbacks by api key hash and project while allowing independent scopes to proceed.
type keyedMutexLockProvider struct {
	guard sync.Mutex
	locks map[string]*sync.Mutex
}

// globalMutexLockProvider serializes all callbacks through one mutex for sqlite-backed tests.
type globalMutexLockProvider struct {
	guard sync.Mutex
}

// WithProjectLock acquires a per-scope mutex, opens a transaction, and commits or rolls back around the callback.
func (p *keyedMutexLockProvider) WithProjectLock(ctx context.Context, db *sql.DB, _ bool, apiKeyHash, project string, _ time.Duration, fn func(tx *sql.Tx) error) error {
	p.guard.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	scope := apiKeyHash + ":" + project
	lock, ok := p.locks[scope]
	if !ok {
		lock = &sync.Mutex{}
		p.locks[scope] = lock
	}
	p.guard.Unlock()

	lock.Lock()
	defer lock.Unlock()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// WithProjectLock executes the callback under a single mutex to avoid sqlite table-lock false negatives.
func (p *globalMutexLockProvider) WithProjectLock(ctx context.Context, db *sql.DB, _ bool, _ string, _ string, _ time.Duration, fn func(tx *sql.Tx) error) error {
	p.guard.Lock()
	defer p.guard.Unlock()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// newConcurrentTestService constructs a service with a deterministic lock provider for conflict testing.
func newConcurrentTestService(t *testing.T, settings Settings, lockProvider LockProvider) *Service {
	t.Helper()

	credential, err := NewCredentialProtector(settings.Security)
	require.NoError(t, err)

	svc, err := NewService(
		newTestDB(t),
		settings,
		testEmbedder{vector: pgvector.NewVector([]float32{1, 0})},
		nil,
		credential,
		&memoryCredentialStore{},
		nil,
		lockProvider,
		func() time.Time { return time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC) },
	)
	require.NoError(t, err)
	svc.contextualizer = nil
	return svc
}

// TestConcurrentAppendsPreserveAllWrites verifies conflicting appends do not lose data when serialized by project lock scope.
func TestConcurrentAppendsPreserveAllWrites(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 1_000_000

	svc := newConcurrentTestService(t, settings, &keyedMutexLockProvider{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	const writers = 24
	tokens := make([]string, 0, writers)
	for i := 0; i < writers; i++ {
		tokens = append(tokens, fmt.Sprintf("[%02d]", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), auth, "proj", "/shared.txt", token, "utf-8", 0, WriteModeAppend)
			errCh <- err
		}(token)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	readRes, err := svc.Read(context.Background(), auth, "proj", "/shared.txt", 0, -1)
	require.NoError(t, err)
	for _, token := range tokens {
		require.Equal(t, 1, strings.Count(readRes.Content, token), "token %s missing or duplicated in %q", token, readRes.Content)
	}
	require.Len(t, readRes.Content, writers*4)
}

// TestConcurrentTenantWritesRemainIsolated verifies conflicting writes on identical paths remain isolated by api key hash.
func TestConcurrentTenantWritesRemainIsolated(t *testing.T) {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 1_000_000

	svc := newConcurrentTestService(t, settings, &globalMutexLockProvider{})
	authA := AuthContext{APIKeyHash: "hash-a", APIKey: "key-a", UserIdentity: "user:a"}
	authB := AuthContext{APIKeyHash: "hash-b", APIKey: "key-b", UserIdentity: "user:b"}

	writeMany := func(auth AuthContext, prefix string) []string {
		parts := make([]string, 0, 12)
		for i := 0; i < 12; i++ {
			parts = append(parts, fmt.Sprintf("%s-%02d|", prefix, i))
		}
		return parts
	}
	partsA := writeMany(authA, "A")
	partsB := writeMany(authB, "B")

	var wg sync.WaitGroup
	errCh := make(chan error, len(partsA)+len(partsB))
	for _, token := range partsA {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), authA, "proj", "/shared.txt", token, "utf-8", 0, WriteModeAppend)
			errCh <- err
		}(token)
	}
	for _, token := range partsB {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), authB, "proj", "/shared.txt", token, "utf-8", 0, WriteModeAppend)
			errCh <- err
		}(token)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	readA, err := svc.Read(context.Background(), authA, "proj", "/shared.txt", 0, -1)
	require.NoError(t, err)
	readB, err := svc.Read(context.Background(), authB, "proj", "/shared.txt", 0, -1)
	require.NoError(t, err)

	for _, token := range partsA {
		require.Equal(t, 1, strings.Count(readA.Content, token))
		require.Zero(t, strings.Count(readB.Content, token))
	}
	for _, token := range partsB {
		require.Equal(t, 1, strings.Count(readB.Content, token))
		require.Zero(t, strings.Count(readA.Content, token))
	}
}
