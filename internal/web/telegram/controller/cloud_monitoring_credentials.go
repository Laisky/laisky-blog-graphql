package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"sync"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
)

const (
	// maxMirroredAlertCredentials bounds the alert types the directory copies.
	// Alert types are self-service, so storage could hold more; the oldest are
	// kept, and a credential beyond them cannot be verified.
	maxMirroredAlertCredentials = 10000
	// alertCredentialRefreshInterval is the least time between directory reads,
	// so guessing costs storage at most one read per interval at any rate.
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
)

// credentialVerdict is what the directory can tell about one credential.
type credentialVerdict int

const (
	// credentialUnknown matches no alert type in a current, complete copy, so
	// the credential is refused without a storage lookup.
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
// credential, held as keyed digests, so a refused credential is decided without
// a storage lookup and guessing has no shared budget to spend. It never
// authenticates on its own: a credential it knows is still checked against
// storage, so a revoked token is refused at once. A credential it does not know
// rereads storage, at most once per interval, before it is refused, so a new
// or rotated token is accepted on its first use once that interval has passed.
type alertCredentialDirectory struct {
	load     alertCredentialLoader
	now      func() time.Time
	interval time.Duration
	limit    int
	key      []byte
	reads    singleflight.Group

	truncationLog rate.Sometimes

	mu sync.Mutex
	// digests holds the keyed digest of every copied name and token pair.
	digests map[[sha256.Size]byte]struct{}
	// healthy is whether the last read succeeded, complete whether it copied
	// every alert type. Only a healthy, complete copy can refuse a credential.
	healthy, complete bool
	// readAt is when the last read started. Zero lets the next miss read.
	readAt time.Time
	// reading is set while a read runs, so a miss waits for it rather than
	// answering from the copy it is replacing.
	reading bool
}

// newAlertCredentialDirectory returns an empty directory that reads through load.
func newAlertCredentialDirectory(load alertCredentialLoader, limit int, interval time.Duration) *alertCredentialDirectory {
	key := make([]byte, sha256.Size)
	// crypto/rand.Read never returns an error since Go 1.24.
	_, _ = rand.Read(key)
	return &alertCredentialDirectory{
		load:          load,
		now:           time.Now,
		interval:      interval,
		limit:         limit,
		key:           key,
		truncationLog: rate.Sometimes{Interval: alertCredentialTruncationLogInterval},
	}
}

// check returns what the directory can tell about a Basic credential. It trims
// both parts, as ValidateTokenForAlertType does, so the copy never refuses a
// credential storage would accept.
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
	d.refresh(ctx, logger)
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

// currentLocked reports whether no read is running and the last one started
// within the interval. d.mu must be held.
func (d *alertCredentialDirectory) currentLocked() bool {
	return !d.reading && !d.readAt.IsZero() && d.now().Before(d.readAt.Add(d.interval))
}

// refresh rereads storage unless a read started within the interval. Concurrent
// callers share one read, and a caller whose request ends stops waiting
// without canceling the read for the others. A failed read keeps the previous
// copy, so known credentials still reach storage, but refuses nothing until a
// later read succeeds.
func (d *alertCredentialDirectory) refresh(ctx context.Context, logger glog.Logger) {
	results := d.reads.DoChan("read", func() (any, error) {
		d.read(context.WithoutCancel(ctx), logger)
		// read reports through the directory's state and its own log, so the
		// shared result carries nothing.
		return struct{}{}, nil
	})
	select {
	case <-results:
	case <-ctx.Done():
	}
}

// read replaces the copy with storage's alert types unless a read started
// within the interval. Only one read runs at a time; refresh ensures that. Its
// errors are logged here, once per read, rather than by every waiting request.
func (d *alertCredentialDirectory) read(ctx context.Context, logger glog.Logger) {
	d.mu.Lock()
	if d.currentLocked() {
		d.mu.Unlock()
		return
	}
	d.readAt, d.reading = d.now(), true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.reading = false
		d.mu.Unlock()
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
		digests[d.digest(alert.Name, alert.PushToken)] = struct{}{}
	}
	d.mu.Lock()
	d.digests, d.healthy, d.complete = digests, true, complete
	d.mu.Unlock()
	logger.Debug("read alert credential directory", zap.Int("alert_types", len(alerts)), zap.Bool("complete", complete))
	if !complete {
		d.truncationLog.Do(func() {
			logger.Error("alert credential directory holds only the oldest alert types; newer ones answer 503",
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
