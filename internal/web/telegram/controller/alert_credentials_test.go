package telegram

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// steppedClock is a manually advanced clock for a credential directory.
type steppedClock struct{ current atomic.Int64 }

// Now returns the clock's current instant.
func (c *steppedClock) Now() time.Time { return time.Unix(0, c.current.Load()).UTC() }

// advance moves the clock forward by d.
func (c *steppedClock) advance(d time.Duration) { c.current.Add(int64(d)) }

// newDirectoryResolver returns a resolver whose credential directory runs on a
// manual clock and a manual count of in-process credential changes.
func newDirectoryResolver(t *testing.T, svc service.Interface) (*MutationResolver, *steppedClock, *atomic.Uint64) {
	t.Helper()
	resolver := NewMutationResolver(svc)
	require.NotNil(t, resolver.credentials)
	clock := new(steppedClock)
	clock.current.Store(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC).UnixNano())
	changes := new(atomic.Uint64)
	resolver.credentials.now = clock.Now
	resolver.credentials.changes = changes.Load
	return resolver, clock, changes
}

// TestAlertAnonymousFloodSpendsNoSharedBudget bounds unknown credential lookups without spending delivery capacity.
func TestAlertAnonymousFloodSpendsNoSharedBudget(t *testing.T) {
	// Three shared sends that do not refill while the test runs.
	configureAlertThrottle(t, 3, 3, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}
	resolver := NewMutationResolver(svc)

	for i := range 100 {
		alertType := fmt.Sprintf("probe-%03d", i)
		_, err := resolver.TelegramMonitorAlert(t.Context(), alertType, "anonymous", "flood")
		require.Error(t, err)

		_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", fmt.Sprintf("guess-%03d", i), "forged")
		require.Error(t, err)
	}
	require.LessOrEqual(t, svc.lookups(), 5, "fallback lookups are bounded")
	require.LessOrEqual(t, svc.directoryReads(), 2, "a flood costs at most one directory read per second")

	for i := range 3 {
		_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", fmt.Sprintf("genuine-%d", i))
		require.NoError(t, err, "the flood must not spend the shared budget")
	}
	require.LessOrEqual(t, svc.lookups(), 8)
}

// TestAlertDirectoryWrongTokenHoldsNoSend keeps unauthenticated guesses from holding a delivery reservation.
func TestAlertDirectoryWrongTokenHoldsNoSend(t *testing.T) {
	configureAlertThrottle(t, 1, 1, 1, 1)
	svc := &tokenGatedTelegram{
		tokens:    map[string]string{"known_alert": "known-token"},
		holdToken: "known-token",
		entered:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	resolver := NewMutationResolver(svc)

	genuine := make(chan error, 1)
	go func() {
		_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
		genuine <- err
	}()
	<-svc.entered

	// Both budgets are now reserved by the genuine request in flight.
	for range 10 {
		_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "wrong-token", "forged")
		require.Error(t, err)
	}
	close(svc.release)
	require.NoError(t, <-genuine)
	require.LessOrEqual(t, svc.lookups(), 6)
}

// TestAlertDirectoryAcceptsInProcessCredentialAtOnce covers in-process and remote credential creation.
func TestAlertDirectoryAcceptsInProcessCredentialAtOnce(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}
	resolver, clock, changes := newDirectoryResolver(t, svc)

	_, err := resolver.TelegramMonitorAlert(t.Context(), "missing_alert", "token", "probe")
	require.ErrorContains(t, err, "not found")
	require.Equal(t, 1, svc.directoryReads())

	svc.setToken("bot_alert", "bot-token")
	changes.Add(1)
	_, err = resolver.TelegramMonitorAlert(t.Context(), "bot_alert", "bot-token", "first use")
	require.NoError(t, err, "an alert type this process created must work on its first use")
	require.Equal(t, 2, svc.directoryReads())

	svc.setToken("replica_alert", "replica-token")
	_, err = resolver.TelegramMonitorAlert(t.Context(), "replica_alert", "replica-token", "too soon")
	require.NoError(t, err, "another process's new type is checked against storage")
	clock.advance(alertCredentialRefreshInterval)
	_, err = resolver.TelegramMonitorAlert(t.Context(), "replica_alert", "replica-token", "after the interval")
	require.NoError(t, err)
	require.Equal(t, 3, svc.directoryReads())
}

// TestAlertDirectoryRotatedTokenWorksOnFirstUse covers a token rotated since the
// directory read: storage refuses the old pair the directory still knew, which
// drops it, and the new token rereads and works on its first use.
func TestAlertDirectoryRotatedTokenWorksOnFirstUse(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 10, 10)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "old-token"}}
	resolver, _, _ := newDirectoryResolver(t, svc)

	_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "old-token", "before rotation")
	require.NoError(t, err)

	svc.setToken("known_alert", "new-token")
	_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", "old-token", "stale")
	require.EqualError(t, err, "token invalidate for `known_alert`")
	require.Equal(t, 2, svc.lookups(), "the stale pair the directory knew is checked against storage once")

	_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", "new-token", "after rotation")
	require.NoError(t, err, "the rotated token must work on its first use")

	_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", "old-token", "stale again")
	require.EqualError(t, err, "token invalidate for `known_alert`")
	require.Equal(t, 4, svc.lookups(), "a miss is checked against storage through the fallback budget")
}

// TestAlertDirectoryUnverifiableFallsBackToStorage verifies bounded lookup after a failed or truncated read.
func TestAlertDirectoryUnverifiableFallsBackToStorage(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  *tokenGatedTelegram
	}{
		{"read fails", &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}, directoryErr: errors.New("synthetic directory outage")}},
		{"copy truncated", &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}, truncated: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureAlertThrottle(t, 100, 100, 100, 100)
			resolver := NewMutationResolver(tc.svc)

			for range 4 {
				_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "wrong-token", "forged")
				require.EqualError(t, err, "token invalidate for `known_alert`")
			}
			_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
			require.NoError(t, err)
			require.Equal(t, 5, tc.svc.lookups())
			require.Equal(t, 1, tc.svc.directoryReads(), "storage refusals must not force directory rereads")
		})
	}
}

// TestAlertDirectoryReadPanicIsContained reproduces a crash: the directory read
// runs on a goroutine shared by every waiting request, where a panicking loader
// ended the whole process. It now fails that read, and the request goes on.
func TestAlertDirectoryReadPanicIsContained(t *testing.T) {
	directory := newAlertCredentialDirectory(func(context.Context, int) ([]*model.AlertTypes, bool, error) {
		panic("synthetic loader panic")
	}, 10, time.Second)

	require.Equal(t, credentialUnverifiable, directory.check(t.Context(), glog.Shared, "known_alert", "token"))
	require.False(t, directory.knowsName("known_alert"))
}

// TestCloudMonitoringWebhookSharesMutationDirectory keeps one credential copy
// per process: the webhook built from the GraphQL mutation's resolver uses its
// directory, so one read and one rotation view serve both entrypoints.
func TestCloudMonitoringWebhookSharesMutationDirectory(t *testing.T) {
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}}
	resolver := NewMutationResolver(svc)
	webhook := resolver.CloudMonitoringWebhook()
	require.Same(t, resolver.credentials, webhook.credentials)
	require.Same(t, resolver, webhook.sender)

	var unset *tokenGatedTelegram
	require.Nil(t, NewMutationResolver(unset).credentials, "a nil service pointer builds no directory")
	require.Nil(t, NewCloudMonitoringWebhook(unset).svc)
}
