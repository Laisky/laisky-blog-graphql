package telegram

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// storedCredentials is a concurrency-safe alert type store for a directory loader.
type storedCredentials struct {
	mu     sync.Mutex
	tokens map[string]string
	err    error
	reads  int
}

// load copies the stored credentials, or fails with err when it is set.
func (s *storedCredentials) load(context.Context, int) ([]*model.AlertTypes, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.err != nil {
		return nil, false, s.err
	}
	alerts := make([]*model.AlertTypes, 0, len(s.tokens))
	for name, token := range s.tokens {
		alerts = append(alerts, &model.AlertTypes{Name: name, PushToken: token})
	}
	return alerts, true, nil
}

// set stores or rotates one alert type's token.
func (s *storedCredentials) set(name, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[name] = token
}

// fail makes later reads fail with err, or succeed again when err is nil.
func (s *storedCredentials) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// readCount returns how many reads ran.
func (s *storedCredentials) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// newSteppedDirectory returns a directory over store that runs on a manual clock
// and a manual count of in-process credential changes.
func newSteppedDirectory(store *storedCredentials) (*alertCredentialDirectory, *steppedClock, *atomic.Uint64) {
	directory := newAlertCredentialDirectory(store.load, 10, alertCredentialRefreshInterval)
	clock := new(steppedClock)
	clock.current.Store(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).UnixNano())
	changes := new(atomic.Uint64)
	directory.now = clock.Now
	directory.changes = changes.Load
	return directory, clock, changes
}

// TestAlertCredentialDirectoryAnonymousMissesCannotDriveFrequentReads reproduces
// review finding 1: once the copy was a second old, any miss started a full
// directory read, so an anonymous caller sending one unknown credential a second
// kept MongoDB scanning every alert type every second. A healthy copy is now
// reread at most once per alertCredentialRefreshInterval, whatever the miss rate.
func TestAlertCredentialDirectoryAnonymousMissesCannotDriveFrequentReads(t *testing.T) {
	store := &storedCredentials{tokens: map[string]string{"known_alert": "known-token"}}
	directory, clock, _ := newSteppedDirectory(store)

	for i := range 60 {
		require.Equal(t, credentialUnknown, directory.check(t.Context(), glog.Shared, "probe-"+strconv.Itoa(i), "guess"))
		clock.advance(time.Second)
	}
	require.LessOrEqual(t, store.readCount(), 3, "a minute of anonymous misses costs at most one full read per 30 seconds")
	require.Equal(t, credentialKnown, directory.check(t.Context(), glog.Shared, "known_alert", "known-token"))
}

// TestAlertCredentialDirectoryRereadsPromptlyWhenItMatters keeps the rereads that
// let a genuine credential work: a failed read is retried after a second; a
// credential this process created or re-keyed is read on the next miss, at most
// once a second; and a pair storage refused is reread at once.
func TestAlertCredentialDirectoryRereadsPromptlyWhenItMatters(t *testing.T) {
	store := &storedCredentials{tokens: map[string]string{"known_alert": "known-token"}, err: errors.New("synthetic outage")}
	directory, clock, changes := newSteppedDirectory(store)

	require.Equal(t, credentialUnverifiable, directory.check(t.Context(), glog.Shared, "known_alert", "known-token"))
	store.fail(nil)
	clock.advance(time.Second)
	require.Equal(t, credentialKnown, directory.check(t.Context(), glog.Shared, "known_alert", "known-token"), "a failed read is retried after a second")
	require.Equal(t, 2, store.readCount())

	clock.advance(time.Second)
	store.set("bot_alert", "bot-token")
	changes.Add(1)
	require.Equal(t, credentialKnown, directory.check(t.Context(), glog.Shared, "bot_alert", "bot-token"), "a local change is read on the next miss")
	require.Equal(t, 3, store.readCount())

	store.set("known_alert", "rotated-token")
	directory.forget("known_alert", "known-token")
	require.Equal(t, credentialKnown, directory.check(t.Context(), glog.Shared, "known_alert", "rotated-token"), "a storage refusal rereads at once")
	require.Equal(t, 4, store.readCount())
}

// TestAlertFallbackVerifiedCredentialIsRemembered covers the other half of
// finding 1. With rereads no longer driven by misses, a credential the copy
// lacks, such as one another process created, must not depend on the fallback
// for every use until the next read. Storage's acceptance through the fallback
// is remembered, so its next uses are known and spend no fallback lookup. A
// refused pair is never remembered.
func TestAlertFallbackVerifiedCredentialIsRemembered(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}
	resolver, _, _ := newDirectoryResolver(t, svc)
	// Two fallback lookups that do not refill while the test runs.
	resolver.credentials.fallback = rate.NewLimiter(0, 2)
	require.Equal(t, credentialKnown, resolver.credentials.check(t.Context(), glog.Shared, "known_alert", "known-token"))

	svc.setToken("remote_alert", "remote-token")
	_, err := resolver.TelegramMonitorAlert(t.Context(), "remote_alert", "remote-token", "first")
	require.NoError(t, err)
	_, err = resolver.TelegramMonitorAlert(t.Context(), "remote_alert", "wrong-token", "forged")
	require.ErrorIs(t, err, service.ErrAlertUnauthorized)

	_, err = resolver.TelegramMonitorAlert(t.Context(), "remote_alert", "remote-token", "second")
	require.NoError(t, err, "a verified credential needs no further fallback lookup")
	webhook := resolver.CloudMonitoringWebhook()
	require.Equal(t, credentialKnown, webhook.credentials.check(t.Context(), glog.Shared, "remote_alert", "remote-token"))
	_, err = resolver.TelegramMonitorAlert(t.Context(), "remote_alert", "wrong-token", "forged")
	require.ErrorIs(t, err, errAlertThrottled, "a refused pair is not remembered, so it still needs the spent fallback")
	require.Equal(t, 1, svc.directoryReads())
}

// TestAlertCredentialDirectoryReadDeadlineHoldsWhenLoaderOverruns reproduces
// review finding 2. The read deadline was only as good as the loader's respect
// for it, and the DAO closed its cursor with a context that had no deadline.
// Cleanup on a stalled connection could therefore outlast the read's five
// seconds, and until it returned, waiting misses stayed held. The deadline now
// ends the read for every waiter on its own. The overrunning loader may finish
// in its own time, and no second loader starts beside it.
func TestAlertCredentialDirectoryReadDeadlineHoldsWhenLoaderOverruns(t *testing.T) {
	stall := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(stall) }) }
	t.Cleanup(release)
	var loads atomic.Int32
	var overrun atomic.Bool
	overrun.Store(true)
	directory := newAlertCredentialDirectory(func(ctx context.Context, _ int) ([]*model.AlertTypes, bool, error) {
		loads.Add(1)
		if overrun.Load() {
			<-ctx.Done()
			// Cleanup that ignores the expired context, as a cursor close
			// without a deadline on a stalled connection does.
			<-stall
			return nil, false, errors.WithStack(ctx.Err())
		}
		return []*model.AlertTypes{{Name: "known_alert", PushToken: "known-token"}}, true, nil
	}, 10, time.Hour)
	directory.loadTimeout = 50 * time.Millisecond
	clock := new(steppedClock)
	directory.now = clock.Now
	directory.changes = func() uint64 { return 0 }

	answered := make(chan credentialVerdict, 1)
	go func() { answered <- directory.check(context.Background(), glog.Shared, "known_alert", "known-token") }()
	select {
	case verdict := <-answered:
		require.Equal(t, credentialUnverifiable, verdict)
	case <-time.After(40 * directory.loadTimeout):
		t.Fatal("a waiting miss outlived the read deadline while the loader overran it")
	}

	clock.advance(2 * time.Second)
	started := time.Now()
	require.Equal(t, credentialUnverifiable, directory.check(context.Background(), glog.Shared, "known_alert", "known-token"))
	require.Less(t, time.Since(started), 20*directory.loadTimeout, "a miss does not wait for an overrunning loader")
	require.EqualValues(t, 1, loads.Load(), "no second loader starts beside an overrunning one")

	overrun.Store(false)
	release()
	require.Eventually(t, func() bool {
		return directory.check(context.Background(), glog.Shared, "known_alert", "known-token") == credentialKnown
	}, 2*time.Second, 5*time.Millisecond, "a read starts again once the overrunning loader returns")
	require.EqualValues(t, 2, loads.Load())
	directory.mu.Lock()
	stats := directory.stats
	directory.mu.Unlock()
	require.EqualValues(t, 1, stats.TimedOut)
	require.EqualValues(t, 1, stats.Late, "the overrunning loader's late result was dropped and counted")
}

// TestAlertCredentialDirectoryReadSummary covers the observability half of
// finding 1. Reads are counted by reason, with their failures, the alert types
// they copied, and their duration. A summary is logged after a read at most once
// a minute, so neither reads nor misses set the log volume.
func TestAlertCredentialDirectoryReadSummary(t *testing.T) {
	store := &storedCredentials{tokens: map[string]string{"a": "token-a", "b": "token-b"}}
	directory, clock, _ := newSteppedDirectory(store)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.Lock(zapcore.AddSync(&output)), zap.DebugLevel)
	logger, err := glog.NewWithName("alert_credential_summary_test", glog.LevelDebug,
		zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)

	require.Equal(t, credentialUnknown, directory.check(t.Context(), logger, "probe", "guess"))
	store.fail(errors.New("synthetic outage"))
	clock.advance(alertCredentialRefreshInterval)
	require.Equal(t, credentialUnverifiable, directory.check(t.Context(), logger, "probe", "guess"))
	store.fail(nil)
	clock.advance(alertCredentialRetryInterval)
	require.Equal(t, credentialUnknown, directory.check(t.Context(), logger, "probe", "guess"))

	directory.mu.Lock()
	stats := directory.stats
	directory.mu.Unlock()
	require.EqualValues(t, 3, stats.Reads)
	require.EqualValues(t, 1, stats.ByInitial)
	require.EqualValues(t, 1, stats.ByInterval)
	require.EqualValues(t, 1, stats.ByRetry)
	require.EqualValues(t, 1, stats.Failed)
	require.EqualValues(t, 4, stats.AlertTypes)
	require.Equal(t, 2, stats.LastAlertTypes)
	require.Positive(t, stats.MaxDuration)

	require.NoError(t, logger.Sync())
	require.Equal(t, 1, strings.Count(output.String(), `"msg":"alert credential directory reads"`), output.String())
	for _, field := range []string{`"reads_total":1`, `"reads_initial":1`, `"alert_types_copied_total":2`, `"last_read_duration":`, `"refresh_interval":30`} {
		require.Contains(t, output.String(), field)
	}
	require.NotContains(t, output.String(), "token-a", "the summary never carries a credential")
}
