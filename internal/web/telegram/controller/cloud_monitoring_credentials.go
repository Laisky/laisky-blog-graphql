package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"golang.org/x/time/rate"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

const (
	// maxMirroredAlertCredentials bounds the alert types the directory copies.
	// Alert types are self-service, so storage could hold more; the oldest are
	// kept, and credentials beyond them require a bounded storage lookup.
	maxMirroredAlertCredentials = 10000
	// alertCredentialRefreshInterval is the least time between reads of a healthy
	// copy. Misses start reads, and anyone can send a miss, so this interval, not
	// the miss rate, sets how often storage is scanned. Meanwhile a credential the
	// copy lacks is verified through the bounded fallback lookup and remembered.
	alertCredentialRefreshInterval = 30 * time.Second
	// alertCredentialRetryInterval is the least time between reads after a failed
	// read, or after this process created or re-keyed an alert type, so either is
	// read on the next miss without letting storage be scanned more than once a
	// second.
	alertCredentialRetryInterval = time.Second
	// alertCredentialLoadTimeout bounds one directory read. The read is shared by
	// every request waiting on it, so it does not end with the request that
	// started it.
	alertCredentialLoadTimeout = 5 * time.Second
	// maxCloudMonitoringCredentialBytes is longer than any alert name or push
	// token storage accepts (100 and 64 characters), so a longer one is refused
	// before it is hashed.
	maxCloudMonitoringCredentialBytes = 512
	// alertCredentialTruncationLogInterval spaces the error about a directory
	// that could not copy every alert type.
	alertCredentialTruncationLogInterval = time.Minute
	// maxAlertCredentialReadWaiters bounds the misses that wait for a running
	// directory read. A miss past the limit answers from the copy as it stands,
	// which sends a pair the copy lacks to the bounded fallback lookup, so a slow
	// read holds at most this many requests. The waiting misses leave room for a
	// cold start's or a rotation's burst of genuine first uses, which then reach
	// storage without spending the fallback budget.
	maxAlertCredentialReadWaiters = 32
	// alertCredentialWaitOverflowLogInterval spaces the warning about misses
	// that found every waiting place taken.
	alertCredentialWaitOverflowLogInterval = time.Minute
)

// credentialVerdict is what the directory can tell about one credential.
type credentialVerdict int

const (
	// credentialUnknown matches no alert type in a current, complete copy. It
	// still requires a bounded authoritative lookup before refusal.
	credentialUnknown credentialVerdict = iota
	// credentialKnown matches an alert type in the copy. Storage still decides.
	credentialKnown
	// credentialUnverifiable cannot be decided: the copy could not be read, or it
	// holds only some of the alert types.
	credentialUnverifiable
)

// alertCredentialLoader reads up to limit alert types, oldest first, and reports
// whether that was every alert type.
type alertCredentialLoader func(ctx context.Context, limit int) (alerts []*model.AlertTypes, complete bool, err error)

// alertCredentialDirectory is an in-memory copy of every alert type's
// credential, held as keyed digests. It never authenticates on its own: a
// credential it knows is still checked against storage, so a revoked token is
// refused at once. A pair it lacks takes a separately bounded authoritative
// lookup, and a pair storage accepts that way is remembered, so a credential
// created or rotated by another process needs that lookup once rather than until
// the next read. Reads are spaced by alertCredentialRefreshInterval whatever the
// request rate, and each is bounded by its own deadline. One directory and
// fallback budget serve both the GraphQL alert mutation and the Cloud Monitoring
// webhook.
type alertCredentialDirectory struct {
	load alertCredentialLoader
	now  func() time.Time
	// interval spaces reads of a healthy copy, and retryInterval reads after a
	// failed read or a local credential change.
	interval, retryInterval time.Duration
	// loadTimeout bounds one read; alertCredentialLoadTimeout outside tests.
	loadTimeout time.Duration
	limit       int
	key         []byte
	// waiters holds one token per miss waiting for a read. A miss that finds it
	// full answers without waiting.
	waiters chan struct{}
	// fallback bounds the rate and concurrency of cache-miss lookups.
	fallback      *rate.Limiter
	fallbackSlots chan struct{}
	// changes counts the alert types this process created or re-keyed.
	changes func() uint64

	truncationLog rate.Sometimes
	// summaryLog spaces the read summary.
	summaryLog rate.Sometimes
	// overflowLog spaces the warning about misses past the waiting limit, and
	// overflows counts them.
	overflowLog rate.Sometimes
	overflows   atomic.Uint64

	mu sync.Mutex
	// digests holds the keyed digest of every copied name and token pair.
	digests map[[sha256.Size]byte]struct{}
	// mutations tracks successful fallback verifications and storage refusals
	// so a read cannot overwrite changes made while its snapshot was in flight.
	mutations uint64
	// healthy is whether the last read succeeded, complete whether it copied
	// every alert type. Only a healthy, complete copy can identify a miss as unknown.
	healthy, complete bool
	// readAt is when the last read started. Zero lets the next miss read.
	readAt time.Time
	// readChanges is what changes returned when the last read started.
	readChanges uint64
	// readDone is closed when the running read ends, and nil while none runs.
	// Every waiting miss selects on the same channel, so waiting allocates
	// nothing per request and a miss that stops waiting leaves nothing behind.
	readDone chan struct{}
	// loading is set from a read's start until its loader returns, which can be
	// after the read's deadline ended it for its waiters. No read starts while it
	// is set, so a loader that overruns its deadline never has company.
	loading bool
	// stats counts reads and remembered credentials for the read summary.
	stats alertCredentialReadStats
}

// newAlertCredentialDirectory returns an empty directory that reads through load.
func newAlertCredentialDirectory(load alertCredentialLoader, limit int, interval time.Duration) *alertCredentialDirectory {
	key := make([]byte, sha256.Size)
	// crypto/rand.Read never returns an error since Go 1.24.
	_, _ = rand.Read(key)
	return &alertCredentialDirectory{
		load:          load,
		waiters:       make(chan struct{}, maxAlertCredentialReadWaiters),
		fallback:      rate.NewLimiter(1, 5),
		fallbackSlots: make(chan struct{}, 5),
		now:           time.Now,
		interval:      interval,
		retryInterval: min(interval, alertCredentialRetryInterval),
		loadTimeout:   alertCredentialLoadTimeout,
		limit:         limit,
		key:           key,
		changes:       service.AlertCredentialChanges,
		truncationLog: rate.Sometimes{Interval: alertCredentialTruncationLogInterval},
		summaryLog:    rate.Sometimes{Interval: alertCredentialSummaryLogInterval},
		overflowLog:   rate.Sometimes{Interval: alertCredentialWaitOverflowLogInterval},
	}
}

// check returns what the directory can tell about a Basic credential. A pair
// the copy lacks starts a read when the copy is not current (see startRead), and
// waits for it only while a waiting place is free (see awaitRead); otherwise it
// answers from the copy as it stands. Either way the caller sends a pair the
// copy lacks to the bounded fallback lookup.
func (d *alertCredentialDirectory) check(ctx context.Context, logger glog.Logger, alertType, token string) credentialVerdict {
	sum, ok := d.pairDigest(alertType, token)
	if !ok {
		return credentialUnknown
	}
	verdict, current := d.lookup(sum)
	if verdict == credentialKnown || current {
		return verdict
	}
	if done := d.startRead(ctx, logger); done != nil {
		d.awaitRead(ctx, logger, done)
	}
	verdict, _ = d.lookup(sum)
	return verdict
}

// lookup answers from the copy as it stands, and reports whether the copy is
// current (see currentLocked), so the answer stands without another read.
func (d *alertCredentialDirectory) lookup(sum [sha256.Size]byte) (verdict credentialVerdict, current bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	current = d.currentLocked()
	if _, ok := d.digests[sum]; ok {
		return credentialKnown, current
	}
	if !d.healthy || !d.complete {
		return credentialUnverifiable, current
	}
	return credentialUnknown, current
}

// currentLocked reports whether the copy needs no read yet: none is running,
// and the last one started within interval, or within retryInterval if it
// failed or this process created or re-keyed an alert type since. A zero readAt,
// before the first read or after storage refused a pair the copy held, needs a
// read at once. d.mu must be held.
func (d *alertCredentialDirectory) currentLocked() bool {
	if d.readDone != nil || d.readAt.IsZero() {
		return false
	}
	spacing := d.interval
	if !d.healthy || d.changes() != d.readChanges {
		spacing = d.retryInterval
	}
	return d.now().Before(d.readAt.Add(spacing))
}

// readReasonLocked names why a read starts now, for the read summary. d.mu
// must be held.
func (d *alertCredentialDirectory) readReasonLocked() alertCredentialReadReason {
	switch {
	case d.stats.Reads == 0:
		return readReasonInitial
	case d.readAt.IsZero():
		return readReasonStorageRefusal
	case !d.healthy:
		return readReasonRetry
	case d.changes() != d.readChanges:
		return readReasonLocalChange
	default:
		return readReasonInterval
	}
}

// startRead returns a channel that is closed when the directory's read ends,
// starting that read unless one is running. It returns nil when the copy is
// current, or when a loader that overran its deadline has not returned yet, so
// the copy answers as it stands. The read runs on its own goroutine, shared by
// every miss, and does not end with the request that started it. Only one
// loader runs at a time.
func (d *alertCredentialDirectory) startRead(ctx context.Context, logger glog.Logger) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readDone != nil {
		return d.readDone
	}
	if d.loading || d.currentLocked() {
		return nil
	}
	reason := d.readReasonLocked()
	done := make(chan struct{})
	d.readAt, d.readChanges, d.readDone, d.loading = d.now(), d.changes(), done, true
	d.stats.started(reason)
	go d.read(context.WithoutCancel(ctx), logger, reason, done, d.mutations)
	return done
}

// awaitRead waits until done is closed or ctx ends, provided one of the
// maxAlertCredentialReadWaiters waiting places is free. Otherwise it returns at
// once, so a slow read holds at most that many requests, as the fallback lookup
// it leads to is bounded too. Misses past the limit are counted and warned about
// at most once per alertCredentialWaitOverflowLogInterval, so a flood cannot set
// the log volume.
func (d *alertCredentialDirectory) awaitRead(ctx context.Context, logger glog.Logger, done <-chan struct{}) {
	select {
	case d.waiters <- struct{}{}:
		defer func() { <-d.waiters }()
	default:
		total := d.overflows.Add(1)
		d.overflowLog.Do(func() {
			logger.Warn("alert credential directory read has more waiting misses than allowed; the rest use the fallback lookup",
				zap.Int("waiting_limit", cap(d.waiters)), zap.Uint64("unwaited_misses_total", total))
		})
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// read replaces the copy with storage's alert types unless a concurrent
// remember or forget changed it, then closes done. The
// read's deadline is enforced here rather than trusted to the loader: once it
// passes, the read fails and its waiters are released even if the loader is
// still blocked, and whatever the loader returns later is dropped. A failed
// read keeps the previous copy, so known credentials still reach storage, but
// the copy no longer tells that a miss is unknown until a later read succeeds.
// Each outcome is logged once, here, before the waiting requests are released,
// rather than by every waiting request.
func (d *alertCredentialDirectory) read(ctx context.Context, logger glog.Logger, reason alertCredentialReadReason, done chan struct{}, mutations uint64) {
	readCtx, cancel := context.WithTimeout(ctx, d.loadTimeout)
	defer cancel()
	var once sync.Once
	// finish publishes the read's outcome, reports it, and releases the
	// waiters, once: either when the loader returns in time or at the deadline.
	// The copy is published before done is closed, so a woken miss sees it.
	finish := func(publish, report func()) {
		once.Do(func() {
			d.mu.Lock()
			publish()
			d.readDone = nil
			d.mu.Unlock()
			report()
			d.logSummary(logger)
			close(done)
		})
	}
	expire := context.AfterFunc(readCtx, func() {
		finish(func() {
			d.healthy = false
			d.stats.Failed++
			d.stats.TimedOut++
		}, func() {
			logger.Error("read alert credential directory",
				zap.Error(errors.Errorf("alert credential directory read did not finish within %s", d.loadTimeout)))
		})
	})

	started := time.Now()
	digests, alerts, complete, err := d.loadDigests(readCtx)
	elapsed := time.Since(started)
	if !expire() {
		// The deadline already ended this read for its waiters. The loader has
		// returned now, so another read may start; this result is dropped.
		d.mu.Lock()
		d.loading = false
		d.stats.Late++
		d.mu.Unlock()
		logger.Warn("drop alert credential directory read that returned after its deadline", zap.Duration("elapsed", elapsed))
		return
	}
	if err != nil {
		finish(func() {
			d.loading, d.healthy = false, false
			d.stats.Failed++
			if errors.Is(err, context.DeadlineExceeded) {
				d.stats.TimedOut++
			}
		}, func() {
			logger.Error("read alert credential directory", zap.Error(err))
		})
		return
	}
	discarded := false
	finish(func() {
		d.loading = false
		if d.mutations != mutations {
			// The snapshot may predate a successful fallback or a refusal. Keep
			// the bounded copy and retry at the failed-read cadence.
			d.healthy = false
			discarded = true
			return
		}
		d.digests, d.healthy, d.complete = digests, true, complete
		d.stats.copied(alerts, elapsed)
	}, func() {
		if discarded {
			logger.Debug("discard alert credential directory snapshot after concurrent credential change")
			return
		}
		logger.Debug("read alert credential directory", zap.String("reason", string(reason)),
			zap.Int("alert_types", alerts), zap.Bool("complete", complete), zap.Duration("elapsed", elapsed))
		if !complete {
			d.truncationLog.Do(func() {
				logger.Error("alert credential directory holds only the oldest alert types; newer ones require bounded storage lookups",
					zap.Int("limit", d.limit))
			})
		}
	})
}

// loadDigests runs the loader and hashes its result. A panic, as from a
// malformed loader result, becomes an error: on the directory's shared read
// goroutine it would otherwise end the process rather than one request.
func (d *alertCredentialDirectory) loadDigests(ctx context.Context) (digests map[[sha256.Size]byte]struct{}, alerts int, complete bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			digests, alerts, complete, err = nil, 0, false, errors.Errorf("alert credential directory read panicked: %v", recovered)
		}
	}()
	loaded, complete, err := d.load(ctx, d.limit)
	if err != nil {
		return nil, 0, false, errors.WithStack(err)
	}
	digests = make(map[[sha256.Size]byte]struct{}, len(loaded))
	for _, alert := range loaded {
		if alert == nil {
			return nil, 0, false, errors.New("alert credential loader returned a nil entry")
		}
		digests[d.digest(alert.Name, alert.PushToken)] = struct{}{}
	}
	return digests, len(loaded), complete, nil
}

// remember adds a pair storage has just accepted through the fallback lookup,
// so its next uses are known and spend no fallback lookup. A later uncontended
// read replaces it with storage's own copy. The copy never grows past the directory's
// limit, and only storage's acceptance adds a pair, so guessing cannot grow it.
func (d *alertCredentialDirectory) remember(alertType, token string) {
	sum, ok := d.pairDigest(alertType, token)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.digests[sum]; ok {
		d.mutations++
		return
	}
	if len(d.digests) >= d.limit {
		return
	}
	if d.digests == nil {
		d.digests = make(map[[sha256.Size]byte]struct{})
	}
	d.digests[sum] = struct{}{}
	d.mutations++
	d.stats.Remembered++
}

// forget drops a credential that storage refused and lets the next miss reread,
// so a token rotated since the last read is accepted on its first use.
func (d *alertCredentialDirectory) forget(alertType, token string) {
	sum, ok := d.pairDigest(alertType, token)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.digests, sum)
	d.mutations++
	d.readAt = time.Time{}
}

// pairDigest returns the digest of a credential, after trimming both parts as
// ValidateTokenForAlertType does, so the copy never refuses a credential storage
// would accept. It reports false for a part that is empty or longer than storage
// accepts, which no alert type can match.
func (d *alertCredentialDirectory) pairDigest(alertType, token string) (sum [sha256.Size]byte, ok bool) {
	alertType, token = strings.TrimSpace(alertType), strings.TrimSpace(token)
	if alertType == "" || token == "" || len(alertType) > maxCloudMonitoringCredentialBytes || len(token) > maxCloudMonitoringCredentialBytes {
		return sum, false
	}
	return d.digest(alertType, token), true
}

// digest returns the keyed digest of one name and token pair. The name's length
// prefix keeps two pairs from sharing an input, and the key keeps a dumped
// digest from being checked against guesses offline.
func (d *alertCredentialDirectory) digest(alertType, token string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, d.key)
	mac.Write(binary.AppendUvarint(nil, uint64(len(alertType))))
	mac.Write([]byte(alertType))
	mac.Write([]byte(token))
	var sum [sha256.Size]byte
	copy(sum[:], mac.Sum(nil))
	return sum
}
