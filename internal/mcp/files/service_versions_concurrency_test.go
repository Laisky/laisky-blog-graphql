package files

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// versionsConcurrentSettings returns Settings sized for concurrency tests.
func versionsConcurrentSettings() Settings {
	settings := LoadSettingsFromConfig()
	settings.Search.Enabled = false
	settings.Security.EncryptionKEKs = map[uint16]string{1: testEncryptionKey()}
	settings.MaxProjectBytes = 1_000_000
	return settings
}

// invariantsAfterRetention asserts the retention rule plus structural invariants.
func invariantsAfterRetention(t *testing.T, svc *Service, auth AuthContext, project, path string) {
	t.Helper()
	versions, err := svc.ListVersions(context.Background(), auth, project, path)
	require.NoError(t, err)

	now := svc.clock()
	if len(versions) > versionRetentionTopN {
		for _, v := range versions {
			require.LessOrEqual(t, now.Sub(v.CreatedAt), versionRetentionWindow,
				"version %d is older than retention window but exceeds top-%d", v.ID, versionRetentionTopN)
		}
	}

	seen := make(map[uint64]struct{}, len(versions))
	for _, v := range versions {
		_, dup := seen[v.ID]
		require.False(t, dup, "duplicate version id %d", v.ID)
		seen[v.ID] = struct{}{}
	}

	for i := 1; i < len(versions); i++ {
		require.False(t, versions[i].CreatedAt.After(versions[i-1].CreatedAt),
			"versions must be ordered newest first")
	}
}

// TestVersions_ConcurrentWritesProduceConsistentHistory exercises many concurrent writers.
func TestVersions_ConcurrentWritesProduceConsistentHistory(t *testing.T) {
	svc := newConcurrentTestService(t, versionsConcurrentSettings(), &globalMutexLockProvider{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	const writers = 10
	tokens := make([]string, 0, writers)
	for i := 0; i < writers; i++ {
		tokens = append(tokens, fmt.Sprintf("w%02d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), auth, "proj", "/shared.txt", token, "utf-8", 0, WriteModeTruncate)
			errCh <- err
		}(token)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/shared.txt")
	require.NoError(t, err)
	require.LessOrEqual(t, len(versions), writers-1)
	invariantsAfterRetention(t, svc, auth, "proj", "/shared.txt")

	current, err := svc.Read(context.Background(), auth, "proj", "/shared.txt", 0, -1)
	require.NoError(t, err)
	matched := false
	for _, token := range tokens {
		if current.Content == token {
			matched = true
			break
		}
	}
	require.True(t, matched, "current content %q does not match any writer token", current.Content)
}

// TestVersions_ConcurrentWriteAndRestore exercises restore racing with writes.
func TestVersions_ConcurrentWriteAndRestore(t *testing.T) {
	svc := newConcurrentTestService(t, versionsConcurrentSettings(), &globalMutexLockProvider{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "AAAA", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(context.Background(), auth, "proj", "/a.txt", "BBBB", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	versionA := versions[0].ID

	const writers = 5
	tokens := make([]string, 0, writers)
	for i := 0; i < writers; i++ {
		tokens = append(tokens, fmt.Sprintf("X%03d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers+1)
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", token, "utf-8", 0, WriteModeTruncate)
			errCh <- err
		}(token)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := svc.RestoreVersion(context.Background(), auth, "proj", "/a.txt", versionA)
		errCh <- err
	}()
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			require.True(t, IsCode(err, ErrCodeNotFound), "unexpected error %v", err)
		}
	}

	current, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	allowed := append([]string{"AAAA"}, tokens...)
	matched := false
	for _, candidate := range allowed {
		if current.Content == candidate {
			matched = true
			break
		}
	}
	require.True(t, matched, "current content %q is unexpected", current.Content)
	require.Len(t, current.Content, 4)

	invariantsAfterRetention(t, svc, auth, "proj", "/a.txt")
}

// TestVersions_ConcurrentWriteAndDelete exercises writes racing with delete.
func TestVersions_ConcurrentWriteAndDelete(t *testing.T) {
	svc := newConcurrentTestService(t, versionsConcurrentSettings(), &globalMutexLockProvider{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", "seed", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	const writers = 6
	tokens := make([]string, 0, writers)
	for i := 0; i < writers; i++ {
		tokens = append(tokens, fmt.Sprintf("z%02d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers+2)
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := svc.Write(context.Background(), auth, "proj", "/a.txt", token, "utf-8", 0, WriteModeTruncate)
			errCh <- err
		}(token)
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := svc.Delete(context.Background(), auth, "proj", "/a.txt", false)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		time.Sleep(time.Microsecond)
		_, err := svc.Delete(context.Background(), auth, "proj", "/a.txt", false)
		errCh <- err
	}()
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			require.True(t, IsCode(err, ErrCodeNotFound), "unexpected error %v", err)
		}
	}

	invariantsAfterRetention(t, svc, auth, "proj", "/a.txt")
}

// TestVersions_ConcurrentRenameAndWrite exercises rename racing with writes.
func TestVersions_ConcurrentRenameAndWrite(t *testing.T) {
	svc := newConcurrentTestService(t, versionsConcurrentSettings(), &globalMutexLockProvider{})
	auth := AuthContext{APIKeyHash: "hash", APIKey: "key", UserIdentity: "user:test"}

	_, err := svc.Write(context.Background(), auth, "proj", "/old.txt", "seed", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)

	const writers = 6
	var renameDone atomic.Bool

	var wg sync.WaitGroup
	errCh := make(chan error, writers+1)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("r%02d", i)
			_, err := svc.Write(context.Background(), auth, "proj", "/old.txt", token, "utf-8", 0, WriteModeTruncate)
			errCh <- err
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := svc.Rename(context.Background(), auth, "proj", "/old.txt", "/new.txt", false)
		renameDone.Store(true)
		errCh <- err
	}()
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			require.True(t,
				IsCode(err, ErrCodeNotFound) || IsCode(err, ErrCodeAlreadyExists),
				"unexpected error %v", err)
		}
	}

	statOld, err := svc.Stat(context.Background(), auth, "proj", "/old.txt")
	require.NoError(t, err)
	statNew, err := svc.Stat(context.Background(), auth, "proj", "/new.txt")
	require.NoError(t, err)
	require.True(t, statOld.Exists || statNew.Exists, "at least one of /old.txt or /new.txt must survive")

	if statOld.Exists {
		invariantsAfterRetention(t, svc, auth, "proj", "/old.txt")
	}
	if statNew.Exists {
		invariantsAfterRetention(t, svc, auth, "proj", "/new.txt")
	}
}
