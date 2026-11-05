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
	// alertCredentialRefreshInterval bounds reads on misses, independently of
	// authoritative lookups. A local credential change can trigger an earlier read.
	alertCredentialRefreshInterval = time.Second
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
// credential, held as keyed digests. It never
// authenticates on its own: a credential it knows is still checked against
// storage, so a revoked token is refused at once. Cache misses may use a
// separately bounded authoritative lookup, which permits a remote rotation to
// work before the next directory refresh. One directory and fallback budget
// serve both the GraphQL alert mutation and the Cloud Monitoring webhook.
type alertCredentialDirectory struct {
	load     alertCredentialLoader
	now      func() time.Time
	interval time.Duration
	limit    int
	key      []byte
	// waiters holds one token per miss waiting for a read. A miss that finds it
	// full answers without waiting.
	waiters chan struct{}
	// fallback bounds the rate and concurrency of cache-miss lookups.
	fallback      *rate.Limiter
	fallbackSlots chan struct{}
	// changes counts the alert types this process created or re-keyed.
	changes func() uint64

	truncationLog rate.Sometimes
	// overflowLog spaces the warning about misses past the waiting limit, and
	// overflows counts them.
	overflowLog rate.Sometimes
	overflows   atomic.Uint64

	mu sync.Mutex
	// digests holds the keyed digest of every copied name and token pair.
	digests map[[sha256.Size]byte]struct{}
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
		limit:         limit,
		key:           key,
		changes:       service.AlertCredentialChanges,
		truncationLog: rate.Sometimes{Interval: alertCredentialTruncationLogInterval},
		overflowLog:   rate.Sometimes{Interval: alertCredentialWaitOverflowLogInterval},
	}
}

// check returns what the directory can tell about a Basic credential. It trims
// both parts, as ValidateTokenForAlertType does, so the copy never refuses a
// credential storage would accept. A pair the copy lacks starts a read unless a
// current one exists, and waits for it only while a waiting place is free (see
// awaitRead); otherwise it answers from the copy as it stands. Either way the
// caller sends a pair the copy lacks to the bounded fallback lookup.
func (d *alertCredentialDirectory) check(ctx context.Context, logger glog.Logger, alertType, token string) credentialVerdict {
	alertType, token = strings.TrimSpace(alertType), strings.TrimSpace(token)
	if alertType == "" || token == "" || len(alertType) > maxCloudMonitoringCredentialBytes || len(token) > maxCloudMonitoringCredentialBytes {
		return credentialUnknown
	}
	sum := d.digest(alertType, token)
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

// lookup answers from the copy as it stands, and reports whether the last read
// finished and started within the interval, so the answer stands without
// another read.
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

// currentLocked reports whether no read is running, the last one started within
// the interval, and this process created or re-keyed no alert type since. d.mu
// must be held.
func (d *alertCredentialDirectory) currentLocked() bool {
	return d.readDone == nil && !d.readAt.IsZero() && d.now().Before(d.readAt.Add(d.interval)) && d.changes() == d.readChanges
}

// startRead returns a channel that is closed when the directory's read ends,
// starting that read unless one is running. It returns nil when a read started
// within the interval and this process changed no credential since, so the copy
// already answers. The read runs on its own goroutine, shared by every miss, and
// does not end with the request that started it. Only one read runs at a time.
func (d *alertCredentialDirectory) startRead(ctx context.Context, logger glog.Logger) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readDone != nil {
		return d.readDone
	}
	if d.currentLocked() {
		return nil
	}
	done := make(chan struct{})
	d.readAt, d.readChanges, d.readDone = d.now(), d.changes(), done
	go d.read(context.WithoutCancel(ctx), logger, done)
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

// read replaces the copy with storage's alert types, then closes done. A failed
// read keeps the previous copy, so known credentials still reach storage, but
// the copy no longer tells that a miss is unknown until a later read succeeds.
// Errors are logged here, once per read, rather than by every waiting request.
// A panic, as from a malformed loader result, fails the read: on this shared
// goroutine it would otherwise end the process rather than one request.
func (d *alertCredentialDirectory) read(ctx context.Context, logger glog.Logger, done chan struct{}) {
	defer func() {
		recovered := recover()
		d.mu.Lock()
		if recovered != nil {
			d.healthy = false
		}
		d.readDone = nil
		d.mu.Unlock()
		// The copy is published before done is closed, so a woken miss sees it.
		close(done)
		if recovered != nil {
			logger.Error("read alert credential directory", zap.Error(errors.Errorf("alert credential directory read panicked: %v", recovered)))
		}
	}()

	readCtx, cancel := context.WithTimeout(ctx, alertCredentialLoadTimeout)
	defer cancel()
	alerts, complete, err := d.load(readCtx, d.limit)
	if err != nil {
		d.mu.Lock()
		d.healthy = false
		d.mu.Unlock()
		logger.Error("read alert credential directory", zap.Error(err))
		return
	}
	digests := make(map[[sha256.Size]byte]struct{}, len(alerts))
	for _, alert := range alerts {
		if alert == nil {
			d.mu.Lock()
			d.healthy = false
			d.mu.Unlock()
			logger.Error("read alert credential directory", zap.Error(errors.New("alert credential loader returned a nil entry")))
			return
		}
		digests[d.digest(alert.Name, alert.PushToken)] = struct{}{}
	}
	d.mu.Lock()
	d.digests, d.healthy, d.complete = digests, true, complete
	d.mu.Unlock()
	logger.Debug("read alert credential directory", zap.Int("alert_types", len(alerts)), zap.Bool("complete", complete))
	if !complete {
		d.truncationLog.Do(func() {
			logger.Error("alert credential directory holds only the oldest alert types; newer ones require bounded storage lookups",
				zap.Int("limit", d.limit))
		})
	}
}

// forget drops a credential that storage refused and lets the next miss reread,
// so a token rotated since the last read is accepted on its first use.
func (d *alertCredentialDirectory) forget(alertType, token string) {
	sum := d.digest(strings.TrimSpace(alertType), strings.TrimSpace(token))
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.digests, sum)
	d.readAt = time.Time{}
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
