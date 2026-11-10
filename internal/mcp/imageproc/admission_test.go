package imageproc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

const mib = int64(1 << 20)

// fakeClock is a manually advanced clock for admission tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newTestBudget returns a 1280 MiB budget on a fake clock.
func newTestBudget() (*DecodeBudget, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	b := NewDecodeBudget(1280 * mib)
	b.now = clock.Now
	return b, clock
}

// asyncAcquire starts acquireFor in the background and reports its outcome.
type pendingAcquire struct {
	done    chan struct{}
	release func()
	err     error
}

func asyncAcquire(ctx context.Context, b *DecodeBudget, owner string, cost int64) *pendingAcquire {
	p := &pendingAcquire{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.release, p.err = b.acquireFor(ctx, owner, cost)
	}()
	return p
}

func (p *pendingAcquire) admitted() bool {
	select {
	case <-p.done:
		return p.err == nil
	default:
		return false
	}
}

// waitQueued blocks until the budget has n queued waiters.
func waitQueued(t *testing.T, b *DecodeBudget, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.queue) == n
	}, 5*time.Second, time.Millisecond)
}

// waitDone waits for an asynchronous acquire to finish.
func waitDone(t *testing.T, p *pendingAcquire) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not finish")
	}
}

// TestDecodeBudgetBackfillsPastLargeWaiter is the review repro: with one
// decode in flight and a near-capacity image queued at the head, a tiny image
// that fits in the ~980 MiB still free must be admitted, not time out.
func TestDecodeBudgetBackfillsPastLargeWaiter(t *testing.T) {
	budget := NewDecodeBudget(DefaultDecodeMemoryBudgetBytes)
	inflight, err := budget.acquireFor(context.Background(), "heavy", 300*mib)
	require.NoError(t, err)
	defer inflight()
	queued := asyncAcquire(t.Context(), budget, "heavy", 1200*mib)
	waitQueued(t, budget, 1)

	small := encodeFixture(t, "png", patternImage("rgba", 64, 64))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = budget.NormalizeFor(ctx, "light", small, "small.png")
	require.NoError(t, err, "a small image that fits must overtake a large waiter")

	inflight()
	waitDone(t, queued)
	require.NoError(t, queued.err)
	queued.release()
}

// TestDecodeBudgetAgedWaiterIsNotStarved checks the other half of the rule:
// once the oldest waiter's backfill window has passed, later waiters can no
// longer take budget it needs, so it is admitted as soon as in-flight decodes
// finish, ahead of everything that arrived after it.
func TestDecodeBudgetAgedWaiterIsNotStarved(t *testing.T) {
	budget, clock := newTestBudget()
	a, err := budget.acquireFor(context.Background(), "a", 300*mib)
	require.NoError(t, err)

	head := asyncAcquire(t.Context(), budget, "heavy", 1200*mib)
	waitQueued(t, budget, 1)

	// Within the window a small decode backfills.
	earlyCtx, cancelEarly := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelEarly()
	early, err := budget.acquireFor(earlyCtx, "b", 100*mib)
	require.NoError(t, err)

	clock.Advance(DefaultDecodeBackfillWindow)
	late := asyncAcquire(t.Context(), budget, "c", 100*mib)
	waitQueued(t, budget, 2)
	require.False(t, late.admitted(), "after the window, later waiters may not eat into the oldest waiter's need")

	a()
	require.False(t, head.admitted(), "100 MiB still in flight")
	early()
	waitDone(t, head)
	require.NoError(t, head.err)
	require.False(t, late.admitted(), "the oldest waiter goes first")

	head.release()
	waitDone(t, late)
	require.NoError(t, late.err)
	late.release()
}

// TestDecodeBudgetCapsDecodesPerOwner checks rule 1: one owner cannot have
// more than DefaultDecodeOwnerLimit decodes admitted or queued, however much
// budget is free, while other owners are unaffected.
func TestDecodeBudgetCapsDecodesPerOwner(t *testing.T) {
	budget, _ := newTestBudget()
	r1, err := budget.acquireFor(context.Background(), "heavy", mib)
	require.NoError(t, err)
	r2, err := budget.acquireFor(context.Background(), "heavy", mib)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = budget.acquireFor(ctx, "heavy", mib)
	require.True(t, errs.Is(err, ErrDecodeBusy), "got %v", err)

	other, err := budget.acquireFor(context.Background(), "light", mib)
	require.NoError(t, err, "another owner is not limited by heavy's cap")
	other()

	third := asyncAcquire(t.Context(), budget, "heavy", mib)
	time.Sleep(20 * time.Millisecond)
	require.False(t, third.admitted())
	r1()
	waitDone(t, third)
	require.NoError(t, third.err)
	third.release()
	r2()

	budget.mu.Lock()
	defer budget.mu.Unlock()
	require.Empty(t, budget.owners, "owner bookkeeping is released")
	require.Zero(t, budget.used)
}

// TestDecodeBudgetHeavyCallerCannotStallLightCaller: a caller keeping the
// budget busy with near-capacity images delays another caller's small image
// by at most its own in-flight decode, not until the small one's deadline.
func TestDecodeBudgetHeavyCallerCannotStallLightCaller(t *testing.T) {
	budget := NewDecodeBudget(DefaultDecodeMemoryBudgetBytes)
	budget.backfillWindow = 0 // the heavy waiter is already past its window

	var stop atomic.Bool
	var wg sync.WaitGroup
	for range DefaultDecodeOwnerLimit + 2 {
		wg.Go(func() {
			for !stop.Load() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				release, err := budget.acquireFor(ctx, "heavy", 1200*mib)
				cancel()
				if err == nil {
					time.Sleep(100 * time.Millisecond) // a large decode
					release()
				}
			}
		})
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()
	time.Sleep(50 * time.Millisecond)

	small := encodeFixture(t, "png", patternImage("rgba", 64, 64))
	for range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		start := time.Now()
		_, err := budget.NormalizeFor(ctx, "light", small, "small.png")
		cancel()
		require.NoError(t, err, "the light caller's small image must not end in decode_busy")
		require.Less(t, time.Since(start), 700*time.Millisecond)
	}
}

// TestDecodeBudgetCanceledWaiterUnblocksOthers: removing a canceled head
// re-runs admission, so waiters it was holding back proceed.
func TestDecodeBudgetCanceledWaiterUnblocksOthers(t *testing.T) {
	budget, _ := newTestBudget()
	budget.backfillWindow = 0
	inflight, err := budget.acquireFor(context.Background(), "a", 300*mib)
	require.NoError(t, err)
	defer inflight()

	headCtx, cancelHead := context.WithCancel(context.Background())
	head := asyncAcquire(headCtx, budget, "heavy", 1200*mib)
	waitQueued(t, budget, 1)
	behind := asyncAcquire(t.Context(), budget, "b", 100*mib)
	waitQueued(t, budget, 2)
	require.False(t, behind.admitted())

	cancelHead()
	waitDone(t, head)
	require.True(t, errs.Is(head.err, ErrDecodeBusy), "got %v", head.err)
	waitDone(t, behind)
	require.NoError(t, behind.err)
	behind.release()
}
