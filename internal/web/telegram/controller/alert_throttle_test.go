package telegram

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
)

// tokenGatedTelegram authenticates only the synthetic alert types it stores, and
// fails like the DAO does for an unknown type or a wrong push token.
type tokenGatedTelegram struct {
	recordingTelegram
	tokens    map[string]string
	authCalls int
}

// ValidateTokenForAlertType counts every credential lookup, the MongoDB query in production.
func (s *tokenGatedTelegram) ValidateTokenForAlertType(_ context.Context, token, alertType string) (*model.AlertTypes, error) {
	s.authCalls++
	stored, ok := s.tokens[alertType]
	if !ok {
		return nil, errors.Errorf("alert_type `%s` not found", alertType)
	}
	if token != stored {
		return nil, errors.Errorf("token invalidate for `%s`", alertType)
	}
	return &model.AlertTypes{Name: alertType}, nil
}

// configureAlertThrottle installs an alert rate limiter with the given budgets for one test.
func configureAlertThrottle(t *testing.T, totalPerSec, totalBurst, eachPerSec, eachBurst int) {
	t.Helper()
	resetThrottleConfig(t)
	gconfig.Shared.Set("settings.telegram.throttle.total_per_sec", totalPerSec)
	gconfig.Shared.Set("settings.telegram.throttle.total_burst", totalBurst)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_per_sec", eachPerSec)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_burst", eachBurst)
	NewTelegram(context.Background(), nil)
	require.NotNil(t, telegramRatelimiter)
}

// TestAlertThrottleUnauthenticatedTypesDoNotLeak reproduces the unauthenticated
// resource leak: the mutation used to build a per-type rate limiter, with a refill
// goroutine that never stops, for every distinct type string before checking the
// token, so an anonymous caller grew goroutines and memory without bound.
func TestAlertThrottleUnauthenticatedTypesDoNotLeak(t *testing.T) {
	configureAlertThrottle(t, 100, 100, 100, 100)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_type": "synthetic-token"}}
	resolver := NewMutationResolver(svc)

	const probes = 1000
	before := runtime.NumGoroutine()
	for i := range probes {
		_, err := resolver.TelegramMonitorAlert(context.Background(), fmt.Sprintf("probe-%04d", i), "anonymous", "body")
		require.Error(t, err)
	}
	grown := runtime.NumGoroutine() - before
	t.Logf("goroutines grew by %d after %d unauthenticated alert types", grown, probes)

	require.Less(t, grown, 20, "%d unauthenticated alert types must not leave goroutines behind", probes)
}

// TestAlertThrottleUnauthenticatedCallerCannotDrainKnownType pins that a caller
// who knows an alert type's name but not its token cannot spend that type's
// budget and so block its genuine alerts.
func TestAlertThrottleUnauthenticatedCallerCannotDrainKnownType(t *testing.T) {
	// Each type's budget holds one send and refills after a second, longer than this test runs.
	configureAlertThrottle(t, 100, 100, 1, 1)
	svc := &tokenGatedTelegram{tokens: map[string]string{"known_type": "synthetic-token"}}
	resolver := NewMutationResolver(svc)

	for range 5 {
		_, err := resolver.TelegramMonitorAlert(context.Background(), "known_type", "wrong-token", "forged")
		require.ErrorContains(t, err, "token invalidate for", "a wrong token must be refused by authentication, not by the type's budget")
	}

	alert, err := resolver.TelegramMonitorAlert(context.Background(), "known_type", "synthetic-token", "genuine")
	require.NoError(t, err)
	require.Equal(t, "known_type", alert.Name)
	require.Equal(t, []string{"known_type >>>>>>>>>>>>>>>>>> \ngenuine"}, svc.plain)
}

// TestAlertThrottleBoundsCredentialLookups keeps a flood of unknown types from
// turning into one MongoDB lookup each: the shared budget is spent before the lookup.
func TestAlertThrottleBoundsCredentialLookups(t *testing.T) {
	// The shared budget holds one send and refills after a second.
	configureAlertThrottle(t, 1, 1, 100, 100)
	svc := &tokenGatedTelegram{tokens: map[string]string{}}
	resolver := NewMutationResolver(svc)

	throttled := 0
	for i := range 50 {
		_, err := resolver.TelegramMonitorAlert(context.Background(), fmt.Sprintf("flood-%02d", i), "anonymous", "body")
		require.Error(t, err)
		if err.Error() == "deny by throttle" {
			throttled++
		}
	}

	// One refill is tolerated in case a slow race-detector run crosses a second.
	require.LessOrEqual(t, svc.authCalls, 2)
	require.Equal(t, 50-svc.authCalls, throttled)
}

// TestAlertThrottleExhaustedTypeKeepsSharedBudget keeps one noisy alert type from
// starving the others: once its own budget is empty, its requests are refused
// before the credential lookup and do not spend the shared budget.
func TestAlertThrottleExhaustedTypeKeepsSharedBudget(t *testing.T) {
	// The shared budget starts with two sends; each type starts with one and refills after a second.
	configureAlertThrottle(t, 2, 2, 1, 1)
	svc := &tokenGatedTelegram{tokens: map[string]string{"noisy_type": "noisy-token", "quiet_type": "quiet-token"}}
	resolver := NewMutationResolver(svc)

	_, err := resolver.TelegramMonitorAlert(context.Background(), "noisy_type", "noisy-token", "first")
	require.NoError(t, err)
	require.Equal(t, 1, svc.authCalls)

	for range 10 {
		_, err = resolver.TelegramMonitorAlert(context.Background(), "noisy_type", "noisy-token", "storm")
		require.EqualError(t, err, "deny by throttle")
	}
	require.Equal(t, 1, svc.authCalls, "an exhausted type must be refused before its credentials are looked up")

	_, err = resolver.TelegramMonitorAlert(context.Background(), "quiet_type", "quiet-token", "unrelated")
	require.NoError(t, err, "the noisy type's storm must not spend the shared budget")
	require.Len(t, svc.plain, 2)
}
