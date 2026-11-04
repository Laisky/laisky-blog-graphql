package telegram

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// rateLimiterGoroutines counts goroutines running a go-utils RateLimiter refill loop.
// It reads every goroutine's stack, so unlike a total goroutine count it does not
// move with unrelated runtime or test activity.
func rateLimiterGoroutines(t *testing.T) int {
	t.Helper()
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "go-utils/v6.(*RateLimiter)") {
			count++
		}
	}
	return count
}

// TestAlertThrottlePaddedAliasesShareCanonicalBudget reproduces the review's P1:
// the pre-lookup check used the raw type string while the DAO trims it, so padded
// aliases of an exhausted type skipped the check, spent the shared budget and
// cost a credential lookup each before the per-type step refused them.
func TestAlertThrottlePaddedAliasesShareCanonicalBudget(t *testing.T) {
	// Three shared sends and one send per type; neither refills while the test runs.
	configureAlertThrottle(t, 3, 3, 1, 1)
	svc := &tokenGatedTelegram{tokens: map[string]string{"prod_alert": "prod-token", "quiet_alert": "quiet-token"}}
	resolver := NewMutationResolver(svc)

	_, err := resolver.TelegramMonitorAlert(t.Context(), "prod_alert", "prod-token", "first")
	require.NoError(t, err)
	require.Equal(t, 1, svc.lookups())

	for _, alias := range []string{" prod_alert ", "prod_alert\t", "\nprod_alert", " prod_alert\r\n"} {
		_, err = resolver.TelegramMonitorAlert(t.Context(), alias, "prod-token", "storm")
		require.EqualError(t, err, "deny by throttle", "alias %q", alias)
	}
	require.Equal(t, 1, svc.lookups(), "aliases of an exhausted type must be refused before the credential lookup")

	_, err = resolver.TelegramMonitorAlert(t.Context(), "quiet_alert", "quiet-token", "unrelated")
	require.NoError(t, err, "aliases must not spend the shared budget other types rely on")
}

// TestAlertThrottleRejectsInvalidAlertTypeBeforeAdmission pins that an alert type
// the DAO can never accept is refused before it costs a shared send or a lookup,
// with the DAO's error text, so hashing and storing a ~2 MiB name never happens.
func TestAlertThrottleRejectsInvalidAlertTypeBeforeAdmission(t *testing.T) {
	// One shared send that does not refill while the test runs.
	configureAlertThrottle(t, 1, 1, 1, 1)
	svc := &tokenGatedTelegram{tokens: map[string]string{"quiet_alert": "quiet-token"}}
	resolver := NewMutationResolver(svc)

	for _, tc := range []struct {
		name, alertType, want string
	}{
		{"over the rune limit", strings.Repeat("界", 101), "sanitize alert type: alert name exceeds max length 100"},
		{"megabyte name", strings.Repeat("a", 1<<20), "sanitize alert type: alert name exceeds max length 100"},
		{"blank", " \t\n", "sanitize alert type: alert name is required"},
		{"null byte", "prod\x00alert", "sanitize alert type: alert name contains invalid null byte"},
	} {
		_, err := resolver.TelegramMonitorAlert(t.Context(), tc.alertType, "quiet-token", "body")
		require.EqualError(t, err, tc.want, tc.name)
	}
	require.Zero(t, svc.lookups(), "an invalid alert type must not reach the credential lookup")

	_, err := resolver.TelegramMonitorAlert(t.Context(), "quiet_alert", "quiet-token", "unrelated")
	require.NoError(t, err, "an invalid alert type must not spend the shared send")
}

// TestAlertThrottleConcurrentStormSpendsOneSharedSendPerTypeSend reproduces the
// review's P2: concurrent requests for one type all passed the pre-lookup check
// before any of them spent the type's send, so a concurrent alert storm drained
// the shared budget and ran one lookup per request while only one was delivered.
func TestAlertThrottleConcurrentStormSpendsOneSharedSendPerTypeSend(t *testing.T) {
	const storm = 20
	// The shared budget holds exactly the storm; each type holds one send.
	configureAlertThrottle(t, storm, storm, 1, 1)
	svc := &tokenGatedTelegram{
		tokens:    map[string]string{"storm_alert": "storm-token", "quiet_alert": "quiet-token"},
		holdToken: "storm-token",
		entered:   make(chan struct{}, storm),
		release:   make(chan struct{}),
	}
	resolver := NewMutationResolver(svc)

	errs := make(chan error, storm)
	var wg sync.WaitGroup
	for range storm {
		wg.Go(func() {
			_, err := resolver.TelegramMonitorAlert(t.Context(), "storm_alert", "storm-token", "storm")
			errs <- err
		})
	}

	// Every request either reaches the (held) lookup or returns before it. Only
	// then are the held lookups released, so all requests overlap.
	var results []error
	for entered := 0; entered+len(results) < storm; {
		select {
		case <-svc.entered:
			entered++
		case err := <-errs:
			results = append(results, err)
		}
	}
	close(svc.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		results = append(results, err)
	}

	delivered, throttled := 0, 0
	for _, err := range results {
		if err == nil {
			delivered++
			continue
		}
		require.EqualError(t, err, "deny by throttle")
		throttled++
	}
	require.Equal(t, 1, delivered)
	require.Equal(t, storm-1, throttled)
	require.Equal(t, 1, svc.lookups(), "requests beyond the type's budget must be refused before the lookup")

	_, err := resolver.TelegramMonitorAlert(t.Context(), "quiet_alert", "quiet-token", "unrelated")
	require.NoError(t, err, "a concurrent storm from one type must not drain the shared budget")
}

// TestAlertThrottleUnverifiableDirectoryAdmitsBeforeAuthentication pins the
// degraded contract. When the credential directory cannot decide, because its
// read failed or its copy is truncated, a credential is admitted before storage
// checks it, as before the directory existed. An authentication failure still
// never spends a type's budget, but a wrong token can get the throttle refusal
// when the shared budget or the type's own budget is already empty.
func TestAlertThrottleUnverifiableDirectoryAdmitsBeforeAuthentication(t *testing.T) {
	t.Run("type budget empty", func(t *testing.T) {
		configureAlertThrottle(t, 10, 10, 1, 1)
		svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}, directoryErr: errors.New("synthetic directory outage")}
		resolver := NewMutationResolver(svc)

		_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
		require.NoError(t, err)
		_, err = resolver.TelegramMonitorAlert(t.Context(), "known_alert", "wrong-token", "forged")
		require.EqualError(t, err, "deny by throttle")
		require.Equal(t, 1, svc.lookups(), "the refusal comes before the credential lookup")
	})

	t.Run("shared budget empty", func(t *testing.T) {
		configureAlertThrottle(t, 1, 1, 10, 10)
		svc := &tokenGatedTelegram{tokens: map[string]string{"known_alert": "known-token"}, truncated: true}
		resolver := NewMutationResolver(svc)

		_, err := resolver.TelegramMonitorAlert(t.Context(), "unknown_alert", "wrong-token", "probe")
		require.ErrorContains(t, err, "not found")
		for _, alertType := range []string{"unknown_alert", "known_alert"} {
			_, err = resolver.TelegramMonitorAlert(t.Context(), alertType, "wrong-token", "probe")
			require.EqualError(t, err, "deny by throttle")
		}
		require.Equal(t, 1, svc.lookups())
	})
}

// TestAlertThrottleInFlightLookupHoldsTypeSend pins the admission fairness
// boundary when the credential directory cannot decide. A request then reserves
// its type's send before the credential lookup, so while a lookup is in flight,
// even one with a wrong token, that send is not available to a concurrent
// request. The reservation is returned as soon as the lookup fails, so a wrong
// token never drains the type for later requests. With a working directory a
// wrong token never gets that far (TestAlertDirectoryWrongTokenHoldsNoSend).
func TestAlertThrottleInFlightLookupHoldsTypeSend(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 1, 1)
	svc := &tokenGatedTelegram{
		tokens:       map[string]string{"known_alert": "known-token"},
		holdToken:    "wrong-token",
		entered:      make(chan struct{}, 1),
		release:      make(chan struct{}),
		directoryErr: errors.New("synthetic directory outage"),
	}
	resolver := NewMutationResolver(svc)

	forged := make(chan error, 1)
	go func() {
		_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "wrong-token", "forged")
		forged <- err
	}()
	<-svc.entered

	_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "concurrent")
	require.EqualError(t, err, "deny by throttle", "the in-flight lookup holds the type's only send")

	close(svc.release)
	require.ErrorContains(t, <-forged, "token invalidate for")

	alert, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
	require.NoError(t, err, "the failed lookup must return the reserved send")
	require.Equal(t, "known_alert", alert.Name)
	require.Equal(t, []string{"known_alert >>>>>>>>>>>>>>>>>> \ngenuine"}, svc.plain)
}

// TestAlertThrottleCanceledLookupReturnsTypeSend pins that a request whose
// context ends during the credential lookup does not keep its type's send.
func TestAlertThrottleCanceledLookupReturnsTypeSend(t *testing.T) {
	configureAlertThrottle(t, 10, 10, 1, 1)
	svc := &tokenGatedTelegram{
		tokens:    map[string]string{"known_alert": "known-token"},
		holdToken: "known-token",
		entered:   make(chan struct{}, 2),
		release:   make(chan struct{}),
	}
	resolver := NewMutationResolver(svc)

	ctx, cancel := context.WithCancel(t.Context())
	abandoned := make(chan error, 1)
	go func() {
		_, err := resolver.TelegramMonitorAlert(ctx, "known_alert", "known-token", "abandoned")
		abandoned <- err
	}()
	<-svc.entered
	cancel()
	require.ErrorIs(t, <-abandoned, context.Canceled)

	close(svc.release)
	_, err := resolver.TelegramMonitorAlert(t.Context(), "known_alert", "known-token", "genuine")
	require.NoError(t, err, "an abandoned lookup must return the reserved send")
	require.Equal(t, 2, svc.lookups())
}

// TestValidateTelegramThrottleConfigStartsNoRefillWorker reproduces the review's
// P3: validating the configuration built a live limiter whose refill goroutine
// lived as long as the caller's context.
func TestValidateTelegramThrottleConfigStartsNoRefillWorker(t *testing.T) {
	configureAlertThrottle(t, 10, 20, 5, 10)

	before := rateLimiterGoroutines(t)
	for range 50 {
		require.NoError(t, ValidateTelegramThrottleConfig(t.Context()))
	}
	require.Equal(t, before, rateLimiterGoroutines(t), "validating the configuration must not start refill goroutines")

	gconfig.Shared.Set("settings.telegram.throttle.total_burst", 1)
	require.ErrorContains(t, ValidateTelegramThrottleConfig(t.Context()), "burst")
}
