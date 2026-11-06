package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// TestAlertCredentialRefreshPreservesFallback reproduces a stale read racing a
// successful fallback after a remote token rotation and a full waiter queue.
func TestAlertCredentialRefreshPreservesFallback(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"alert": "old-token"}}
	resolver, _, _ := newDirectoryResolver(t, svc)
	d := resolver.credentials
	snapshot := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	d.load = func(ctx context.Context, limit int) ([]*model.AlertTypes, bool, error) {
		alerts, complete, err := svc.LoadAlertCredentials(ctx, limit)
		close(snapshot)
		<-release
		return alerts, complete, err
	}
	d.fallback = rate.NewLimiter(0, 1)
	done := d.startRead(t.Context(), glog.Shared)
	<-snapshot
	svc.setToken("alert", "new-token")
	for range cap(d.waiters) {
		d.waiters <- struct{}{}
	}
	_, err := resolver.TelegramMonitorAlert(t.Context(), "alert", "new-token", "first")
	require.NoError(t, err)
	require.Equal(t, credentialKnown, d.check(t.Context(), glog.Shared, "alert", "new-token"))
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish")
	}
	_, err = resolver.TelegramMonitorAlert(t.Context(), "alert", "new-token", "second")
	require.NoError(t, err, "the completed old snapshot must not erase the successful fallback")
	svc.setToken("alert", "revoked-token")
	_, err = resolver.TelegramMonitorAlert(t.Context(), "alert", "new-token", "revoked")
	require.ErrorIs(t, err, service.ErrAlertUnauthorized, "storage remains authoritative")
}

// TestAlertCredentialRefreshConcurrentMutation checks refusal, duplicate
// remembers, capacity and the next uncontended refresh.
func TestAlertCredentialRefreshConcurrentMutation(t *testing.T) {
	for _, operation := range []string{"forget", "duplicate"} {
		t.Run(operation, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var loads int
			d := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
				loads++
				if loads == 1 {
					close(entered)
					<-release
				}
				return []*model.AlertTypes{{Name: "a", PushToken: "old"}}, true, nil
			}, 1, time.Hour)
			clock := new(steppedClock)
			clock.current.Store(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).UnixNano())
			d.now, d.changes = clock.Now, func() uint64 { return 0 }
			d.remember("a", "old")
			done := d.startRead(t.Context(), glog.Shared)
			<-entered
			if operation == "forget" {
				d.forget("a", "old")
			} else {
				d.remember("a", "old")
			}
			close(release)
			<-done
			if operation == "forget" {
				v, _ := d.lookup(d.digest("a", "old"))
				require.NotEqual(t, credentialKnown, v, "stale read must not resurrect a refused pair")
			} else {
				require.Equal(t, credentialKnown, d.check(t.Context(), glog.Shared, "a", "old"))
				d.remember("b", "new")
				d.mu.Lock()
				require.Len(t, d.digests, 1, "capacity remains bounded")
				d.mu.Unlock()
			}
			clock.advance(alertCredentialRetryInterval)
			require.Equal(t, credentialKnown, d.check(t.Context(), glog.Shared, "a", "old"))
			// The duplicate case needs a miss to trigger the retry.
			if operation == "duplicate" {
				d.check(t.Context(), glog.Shared, "probe", "token")
			}
			require.Equal(t, 2, loads, "an uncontended retry publishes the snapshot")
		})
	}
}

// TestAlertCredentialRefreshFailureKeepsConcurrentRemember checks failed and
// late reads, neither of which may replace a concurrently remembered pair.
func TestAlertCredentialRefreshFailureKeepsConcurrentRemember(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "deadline"}[timeout], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			d := newAlertCredentialDirectory(func(ctx context.Context, _ int) ([]*model.AlertTypes, bool, error) {
				close(entered)
				<-release
				if timeout {
					return []*model.AlertTypes{{Name: "a", PushToken: "old"}}, true, nil
				}
				return nil, false, context.Canceled
			}, 1, time.Hour)
			d.loadTimeout = 50 * time.Millisecond
			done := d.startRead(t.Context(), glog.Shared)
			<-entered
			d.remember("a", "new")
			if timeout {
				<-done
			}
			close(release)
			<-done
			require.Eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.loading }, time.Second, time.Millisecond)
			verdict, _ := d.lookup(d.digest("a", "new"))
			require.Equal(t, credentialKnown, verdict)
			verdict, _ = d.lookup(d.digest("a", "old"))
			require.NotEqual(t, credentialKnown, verdict)
		})
	}
}

// TestAlertCredentialRefreshFallbackAtCapacity verifies that a successful
// fallback during a read cannot grow a full directory or bypass its budget.
func TestAlertCredentialRefreshFallbackAtCapacity(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"old": "old-token"}}
	resolver, _, _ := newDirectoryResolver(t, svc)
	d := resolver.credentials
	d.limit = 1
	d.remember("old", "old-token")
	d.fallback = rate.NewLimiter(0, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	d.load = func(ctx context.Context, limit int) ([]*model.AlertTypes, bool, error) {
		alerts, complete, err := svc.LoadAlertCredentials(ctx, limit)
		close(entered)
		<-release
		return alerts, complete, err
	}
	done := d.startRead(t.Context(), glog.Shared)
	<-entered
	svc.setToken("remote", "remote-token")
	for range cap(d.waiters) {
		d.waiters <- struct{}{}
	}
	_, err := resolver.TelegramMonitorAlert(t.Context(), "remote", "remote-token", "first")
	require.NoError(t, err)
	d.mu.Lock()
	count := len(d.digests)
	d.mu.Unlock()
	require.Equal(t, 1, count)
	once.Do(func() { close(release) })
	<-done
	verdict, current := d.lookup(d.digest("old", "old-token"))
	require.Equal(t, credentialKnown, verdict)
	require.True(t, current, "a rejected remember must not invalidate an otherwise successful read")
	verdict, _ = d.lookup(d.digest("remote", "remote-token"))
	require.Equal(t, credentialUnknown, verdict)
	_, err = resolver.TelegramMonitorAlert(t.Context(), "remote", "remote-token", "second")
	require.ErrorIs(t, err, errAlertThrottled, "an entry beyond capacity still uses the bounded fallback")
}
