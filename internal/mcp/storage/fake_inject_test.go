package storage

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestFakeStoreInjectIsSafeWhileStoreIsInUse: tests change injected failures
// and hooks while background work (the lifecycle controller, parallel
// uploads) calls the store. A plain field write raced with those reads under
// -race (TestLifecycleControllerReportsEachDistinctProblem failed about once
// in 40 runs). Inject applies a change under the store's lock and every
// method reads the injections under it, so this test is clean under -race.
func TestFakeStoreInjectIsSafeWhileStoreIsInUse(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	failure := errors.New("injected")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = store.CheckLifecycle(ctx, "p/", 7)
			_, _ = store.EnsureLifecycle(ctx, "p/", 7)
			_ = store.Put(ctx, "k", bytes.NewReader([]byte("x")), 1, "image/png", nil)
			_, _, _ = store.Get(ctx, "k")
			_, _ = store.PresignedGet(ctx, "k", 1)
			_ = store.Delete(ctx, "k")
		}
	}()
	for i := range 200 {
		store.Inject(func(s *FakeStore) {
			var err error
			if i%2 == 0 {
				err = failure
			}
			s.LifecycleCheckErr, s.LifecycleErr, s.PutErr, s.GetErr, s.PresignErr, s.DeleteErr = err, err, err, err, err, err
			s.BeforePut, s.AfterPut, s.BeforeDelete = nil, nil, nil
			s.DiscardBodies = i%3 == 0
		})
	}
	close(stop)
	wg.Wait()

	store.Inject(func(s *FakeStore) { s.LifecycleErr, s.LifecycleCheckErr = nil, failure })
	_, err := store.EnsureLifecycle(ctx, "p/", 7)
	require.NoError(t, err)
	require.ErrorIs(t, store.CheckLifecycle(ctx, "p/", 7), failure)
	store.Inject(func(s *FakeStore) { s.LifecycleCheckErr = nil })
	require.NoError(t, store.CheckLifecycle(ctx, "p/", 7), "the recorded rule is found once the failure is cleared")
}
