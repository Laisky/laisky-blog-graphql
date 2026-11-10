package userrequests

import (
	"context"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// tryAcquire acquires with a short deadline and reports whether it got a slot.
func tryAcquire(t *testing.T, l *imageCreateLimiter, user string) (func(), bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	release, err := l.acquire(ctx, user)
	if err != nil {
		require.True(t, errs.Is(err, ErrImageBusy), "got %v", err)
		return nil, false
	}
	return release, true
}

// TestImageCreateLimiterCaps checks the process-wide and per-user caps and
// that a user at its own cap does not block other users queued behind it.
func TestImageCreateLimiterCaps(t *testing.T) {
	l := newImageCreateLimiter(3, 2)
	a1, ok := tryAcquire(t, l, "a")
	require.True(t, ok)
	a2, ok := tryAcquire(t, l, "a")
	require.True(t, ok)
	_, ok = tryAcquire(t, l, "a")
	require.False(t, ok, "user a is at its cap")
	b1, ok := tryAcquire(t, l, "b")
	require.True(t, ok, "user b still fits the global cap")
	_, ok = tryAcquire(t, l, "c")
	require.False(t, ok, "the global cap is reached")

	// A queued waiter of capped user a must not hold back user c.
	waitA := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		release, err := l.acquire(ctx, "a")
		if err == nil {
			release()
		}
		waitA <- err
	}()
	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.waiters) == 1
	}, time.Second, time.Millisecond)
	b1()
	c1, ok := tryAcquire(t, l, "c")
	require.True(t, ok, "the freed slot goes to c, not to a's queued request")
	a1()
	require.NoError(t, <-waitA, "a's queued request runs once a's own slot frees")
	a2()
	c1()

	l.mu.Lock()
	defer l.mu.Unlock()
	require.Zero(t, l.active)
	require.Empty(t, l.users)
	require.Empty(t, l.waiters)
}

// TestImageCreateLimiterCanceledWaiterLeavesQueue checks that a waiter whose
// context ends is removed and does not consume a slot later.
func TestImageCreateLimiterCanceledWaiterLeavesQueue(t *testing.T) {
	l := newImageCreateLimiter(1, 1)
	r, ok := tryAcquire(t, l, "a")
	require.True(t, ok)
	_, ok = tryAcquire(t, l, "b")
	require.False(t, ok)
	l.mu.Lock()
	require.Empty(t, l.waiters, "the canceled waiter left the queue")
	l.mu.Unlock()
	r()
	r2, ok := tryAcquire(t, l, "b")
	require.True(t, ok)
	r2()
}
