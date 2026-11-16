package userrequests

import (
	"context"
	"slices"
	"sync"

	errors "github.com/Laisky/errors/v2"
)

// imageCreateLimiter caps how many image create requests (body intake,
// fetch, decode, upload, and their short database transactions) run at once
// in this process, overall and per user identity. Requests over a cap wait in
// FIFO order, skipping callers whose own identity is at its cap, until a slot
// frees or their context ends. The queue itself is bounded: at most global
// requests wait overall and at most perUser per identity; one beyond that is
// refused at once. The handler takes a slot before it reads an attachment, so
// a waiting request holds no input bytes. It bounds what image traffic can
// occupy (memory, object-store connections, database pool slots), so one
// caller or a slow object store cannot starve unrelated work.
type imageCreateLimiter struct {
	global  int
	perUser int

	mu      sync.Mutex
	active  int
	users   map[string]int
	queued  map[string]int
	waiters []*imageCreateWaiter
}

// imageCreateWaiter is one queued create.
type imageCreateWaiter struct {
	user     string
	ready    chan struct{}
	admitted bool
}

// newImageCreateLimiter returns a limiter; non-positive caps use the defaults.
func newImageCreateLimiter(global, perUser int) *imageCreateLimiter {
	if global <= 0 {
		global = DefaultImageMaxConcurrentCreates
	}
	if perUser <= 0 {
		perUser = DefaultImageMaxConcurrentCreatesPerUser
	}
	return &imageCreateLimiter{global: global, perUser: min(perUser, global), users: make(map[string]int), queued: make(map[string]int)}
}

// acquire takes a slot for user, waiting until one is free or ctx ends
// (ErrImageBusy). When the queue is full overall or for user it fails at once
// with ErrImageBusy. The returned release function must be called once.
func (l *imageCreateLimiter) acquire(ctx context.Context, user string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(ErrImageBusy, err.Error())
	}
	l.mu.Lock()
	w := &imageCreateWaiter{user: user, ready: make(chan struct{})}
	switch {
	case l.eligibleLocked(user):
		l.admitLocked(w)
	case len(l.waiters) >= l.global || l.queued[user] >= l.perUser:
		l.mu.Unlock()
		return nil, errors.Wrap(ErrImageBusy, "image request queue is full")
	default:
		l.waiters = append(l.waiters, w)
		l.queued[user]++
	}
	l.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			l.active--
			if l.users[user]--; l.users[user] <= 0 {
				delete(l.users, user)
			}
			l.dispatchLocked()
			l.mu.Unlock()
		})
	}
	select {
	case <-w.ready:
		return release, nil
	case <-ctx.Done():
	}
	l.mu.Lock()
	if w.admitted {
		l.mu.Unlock()
		release()
	} else {
		l.waiters = slices.DeleteFunc(l.waiters, func(q *imageCreateWaiter) bool { return q == w })
		l.dequeuedLocked(user)
		l.mu.Unlock()
	}
	return nil, errors.Wrap(ErrImageBusy, "too many image requests in flight: "+ctx.Err().Error())
}

// eligibleLocked reports whether user may start now. l.mu must be held.
func (l *imageCreateLimiter) eligibleLocked(user string) bool {
	return l.active < l.global && l.users[user] < l.perUser
}

// admitLocked grants w a slot. l.mu must be held.
func (l *imageCreateLimiter) admitLocked(w *imageCreateWaiter) {
	l.active++
	l.users[w.user]++
	w.admitted = true
	close(w.ready)
}

// dequeuedLocked records that one queued create of user left the queue.
// l.mu must be held.
func (l *imageCreateLimiter) dequeuedLocked(user string) {
	if l.queued[user]--; l.queued[user] <= 0 {
		delete(l.queued, user)
	}
}

// dispatchLocked admits queued creates in arrival order while slots remain,
// skipping those whose user is at its cap. l.mu must be held.
func (l *imageCreateLimiter) dispatchLocked() {
	kept := l.waiters[:0]
	for _, w := range l.waiters {
		if l.eligibleLocked(w.user) {
			l.dequeuedLocked(w.user)
			l.admitLocked(w)
			continue
		}
		kept = append(kept, w)
	}
	clear(l.waiters[len(kept):])
	l.waiters = kept
}
