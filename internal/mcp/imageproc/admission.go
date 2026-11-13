package imageproc

import (
	"context"
	"slices"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// ErrDecodeBusy indicates Normalize could not be admitted to the decode memory
// budget before its context ended because other images were being decoded.
var ErrDecodeBusy = errors.New("image decoder is busy")

const (
	// DefaultDecodeOwnerLimit is how many decodes one owner (user identity)
	// may have admitted or queued in a DecodeBudget at once. Further decodes
	// of that owner wait outside the shared queue.
	DefaultDecodeOwnerLimit = 2
	// DefaultDecodeBackfillWindow is how long the oldest waiter lets later,
	// smaller waiters overtake it while it does not fit. After the window,
	// later waiters may only use budget the oldest waiter does not need.
	DefaultDecodeBackfillWindow = 2 * time.Second
)

// DecodeBudget bounds the summed estimated working set of in-flight Normalize
// calls. An image whose estimate alone exceeds the whole budget is rejected
// with ErrDimensionsTooLarge; waiting honors the caller's context and ends
// with ErrDecodeBusy.
//
// Admission rule (FIFO with bounded backfill and per-owner caps):
//  1. Each owner has at most DefaultDecodeOwnerLimit decodes admitted or
//     queued; its further decodes wait for one of them to finish. The empty
//     owner is not limited.
//  2. Waiters queue in arrival order. The oldest waiter is admitted as soon
//     as it fits. A later waiter that fits in the free budget is admitted
//     ahead of it ("backfill") while the oldest has waited less than
//     DefaultDecodeBackfillWindow; after that, later waiters are admitted
//     only into budget the oldest waiter does not need.
//
// So one large waiter cannot hold small decodes that fit behind it, and it
// cannot be starved either: once its window has passed, the budget it needs
// is only released, never handed out again, so it is admitted when enough
// in-flight decodes finish. A heavy caller can delay another caller's small
// image by at most the time its own admitted decodes take.
//
// A DecodeBudget is safe for concurrent use. One budget should be shared by
// every Normalize caller in a process so the bound is process-wide.
type DecodeBudget struct {
	capacity       int64
	ownerLimit     int
	backfillWindow time.Duration
	now            func() time.Time

	mu     sync.Mutex
	used   int64
	queue  []*decodeWaiter
	owners map[string]*decodeOwner
}

// decodeWaiter is one queued admission request.
type decodeWaiter struct {
	cost     int64
	enqueued time.Time
	ready    chan struct{}
	admitted bool
}

// decodeOwner tracks one owner's admitted-or-queued decodes and the callers
// waiting for one of its slots.
type decodeOwner struct {
	active  int
	waiting []chan struct{}
}

// defaultDecodeBudget backs the package-level Normalize helper.
var defaultDecodeBudget = NewDecodeBudget(DefaultDecodeMemoryBudgetBytes)

// NewDecodeBudget returns a DecodeBudget of capacityBytes with the default
// owner limit and backfill window. Non-positive values fall back to
// DefaultDecodeMemoryBudgetBytes.
func NewDecodeBudget(capacityBytes int64) *DecodeBudget {
	if capacityBytes <= 0 {
		capacityBytes = DefaultDecodeMemoryBudgetBytes
	}
	return &DecodeBudget{
		capacity:       capacityBytes,
		ownerLimit:     DefaultDecodeOwnerLimit,
		backfillWindow: DefaultDecodeBackfillWindow,
		now:            time.Now,
		owners:         make(map[string]*decodeOwner),
	}
}

// Capacity reports the budget size in bytes.
func (b *DecodeBudget) Capacity() int64 {
	if b == nil {
		return DefaultDecodeMemoryBudgetBytes
	}
	return b.capacity
}

// acquireFor reserves cost bytes for owner, blocking until the admission rule
// admits it or ctx ends. The returned release function must be called once.
func (b *DecodeBudget) acquireFor(ctx context.Context, owner string, cost int64) (func(), error) {
	if cost > b.capacity {
		return nil, errors.Wrapf(ErrDimensionsTooLarge,
			"estimated decode working set %d bytes exceeds the %d byte decode memory budget", cost, b.capacity)
	}
	cost = max(cost, 1)
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(ErrDecodeBusy, err.Error())
	}
	if err := b.enterOwner(ctx, owner); err != nil {
		return nil, errors.WithStack(err)
	}

	b.mu.Lock()
	w := &decodeWaiter{cost: cost, enqueued: b.now(), ready: make(chan struct{})}
	b.queue = append(b.queue, w)
	b.dispatchLocked()
	b.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= cost
			b.dispatchLocked()
			b.mu.Unlock()
			b.leaveOwner(owner)
		})
	}

	select {
	case <-w.ready:
		return release, nil
	case <-ctx.Done():
	}
	b.mu.Lock()
	if w.admitted {
		b.mu.Unlock()
		release()
	} else {
		b.queue = slices.DeleteFunc(b.queue, func(q *decodeWaiter) bool { return q == w })
		// The head may have changed, which can admit others.
		b.dispatchLocked()
		b.mu.Unlock()
		b.leaveOwner(owner)
	}
	return nil, errors.Wrap(ErrDecodeBusy, ctx.Err().Error())
}

// dispatchLocked admits every waiter the admission rule allows. b.mu must be held.
func (b *DecodeBudget) dispatchLocked() {
	for len(b.queue) > 0 && b.used+b.queue[0].cost <= b.capacity {
		b.admitLocked(b.queue[0])
		b.queue = b.queue[1:]
	}
	if len(b.queue) < 2 {
		return
	}
	head := b.queue[0]
	limit := b.capacity
	if b.now().Sub(head.enqueued) >= b.backfillWindow {
		limit = b.capacity - head.cost
	}
	kept := make([]*decodeWaiter, 1, len(b.queue))
	kept[0] = head
	for _, w := range b.queue[1:] {
		if b.used+w.cost <= limit {
			b.admitLocked(w)
			continue
		}
		kept = append(kept, w)
	}
	b.queue = kept
}

// admitLocked grants w its budget. b.mu must be held.
func (b *DecodeBudget) admitLocked(w *decodeWaiter) {
	b.used += w.cost
	w.admitted = true
	close(w.ready)
}

// enterOwner takes one of owner's slots, waiting for one to free up.
func (b *DecodeBudget) enterOwner(ctx context.Context, owner string) error {
	if owner == "" {
		return nil
	}
	b.mu.Lock()
	o := b.owners[owner]
	if o == nil {
		o = &decodeOwner{}
		b.owners[owner] = o
	}
	if o.active < b.ownerLimit {
		o.active++
		b.mu.Unlock()
		return nil
	}
	slot := make(chan struct{})
	o.waiting = append(o.waiting, slot)
	b.mu.Unlock()

	select {
	case <-slot:
		return nil // leaveOwner handed its slot over
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-slot:
		// Handed over concurrently with the cancellation: pass it on.
		b.leaveOwnerLocked(owner)
	default:
		o.waiting = slices.DeleteFunc(o.waiting, func(c chan struct{}) bool { return c == slot })
	}
	return errors.Wrap(ErrDecodeBusy, ctx.Err().Error())
}

// leaveOwner returns one of owner's slots.
func (b *DecodeBudget) leaveOwner(owner string) {
	if owner == "" {
		return
	}
	b.mu.Lock()
	b.leaveOwnerLocked(owner)
	b.mu.Unlock()
}

// leaveOwnerLocked hands the slot to the next waiting caller of owner or
// frees it. b.mu must be held.
func (b *DecodeBudget) leaveOwnerLocked(owner string) {
	o := b.owners[owner]
	if o == nil {
		return
	}
	if len(o.waiting) > 0 {
		next := o.waiting[0]
		o.waiting = o.waiting[1:]
		close(next)
		return
	}
	o.active--
	if o.active <= 0 {
		delete(b.owners, owner)
	}
}
