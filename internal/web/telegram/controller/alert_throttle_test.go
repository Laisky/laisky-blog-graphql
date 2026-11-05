package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
)

// tokenGatedTelegram authenticates only the synthetic alert types it stores, and
// fails with the DAO's own refusals for an unknown type or a wrong push token. Like the DAO,
// it trims the requested type before its lookup. It is safe for concurrent use.
type tokenGatedTelegram struct {
	recordingTelegram
	mu        sync.Mutex
	tokens    map[string]string
	authCalls int
	// holdToken, when set, makes lookups that present it block until release is
	// closed, after signaling entered, so a test can keep a lookup in flight.
	holdToken string
	entered   chan struct{}
	release   chan struct{}
	// directoryErr, when set, fails every directory read; truncated reports a
	// copy that holds only some alert types. Either leaves the directory unable
	// to refuse a credential.
	directoryErr error
	truncated    bool
	reads        int
}

// LoadAlertCredentials copies the stored alert types, as the DAO does.
func (s *tokenGatedTelegram) LoadAlertCredentials(context.Context, int) ([]*model.AlertTypes, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.directoryErr != nil {
		return nil, false, s.directoryErr
	}
	alerts := make([]*model.AlertTypes, 0, len(s.tokens))
	for name, token := range s.tokens {
		alerts = append(alerts, &model.AlertTypes{Name: name, PushToken: token})
	}
	return alerts, !s.truncated, nil
}

// setToken stores or rotates an alert type's push token.
func (s *tokenGatedTelegram) setToken(name, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[name] = token
}

// directoryReads returns how many directory reads ran.
func (s *tokenGatedTelegram) directoryReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// ValidateTokenForAlertType counts every credential lookup, the MongoDB query in production.
func (s *tokenGatedTelegram) ValidateTokenForAlertType(ctx context.Context, token, alertType string) (*model.AlertTypes, error) {
	s.mu.Lock()
	s.authCalls++
	hold := s.holdToken != "" && token == s.holdToken
	s.mu.Unlock()
	if hold {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, errors.WithStack(ctx.Err())
		}
	}

	name := strings.TrimSpace(alertType)
	s.mu.Lock()
	stored, ok := s.tokens[name]
	s.mu.Unlock()
	if !ok {
		return nil, service.AlertTypeNotFoundError(name)
	}
	if token != stored {
		return nil, service.AlertTokenMismatchError(name)
	}
	return &model.AlertTypes{Name: name}, nil
}

// SendAlertToUser records the complete alert wire message.
func (s *tokenGatedTelegram) SendAlertToUser(_ context.Context, _ int, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plain = append(s.plain, msg)
	return nil
}

// lookups returns how many credential lookups ran.
func (s *tokenGatedTelegram) lookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authCalls
}

// configureAlertThrottle installs an alert rate limiter with the given budgets for one test.
func configureAlertThrottle(t *testing.T, totalPerSec, totalBurst, eachPerSec, eachBurst int) {
	t.Helper()
	resetThrottleConfig(t)
	gconfig.Shared.Set("settings.telegram.throttle.total_per_sec", totalPerSec)
	gconfig.Shared.Set("settings.telegram.throttle.total_burst", totalBurst)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_per_sec", eachPerSec)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_burst", eachBurst)
	NewTelegram(t.Context(), nil)
	require.NotNil(t, telegramRatelimiter)
}

// TestAlertThrottleUnauthenticatedTypesDoNotLeak reproduces the unauthenticated
// resource leak: the mutation used to build a per-type rate limiter, with a refill
// goroutine that never stops, for every distinct type string before checking the
// token, so an anonymous caller grew goroutines and memory without bound. Neither
// a working nor a degraded directory may leave per-type state behind when
// unknown credentials use the bounded fallback.
func TestAlertThrottleUnauthenticatedTypesDoNotLeak(t *testing.T) {
	const probes = 1000
	for _, tc := range []struct {
		name         string
		directoryErr error
	}{
		{"directory available", nil},
		{"directory unavailable", errors.New("synthetic directory outage")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The delivery budget is large; fallback independently bounds credential lookups.
			configureAlertThrottle(t, 100, probes, 100, 100)
			svc := &tokenGatedTelegram{tokens: map[string]string{"known_type": "synthetic-token"}, directoryErr: tc.directoryErr}
			resolver := NewMutationResolver(svc)
			resolver.credentials.fallback = rate.NewLimiter(0, 5)

			before := rateLimiterGoroutines(t)
			for i := range probes {
				_, err := resolver.TelegramMonitorAlert(context.Background(), fmt.Sprintf("probe-%04d", i), "anonymous", "body")
				require.Error(t, err)
			}

			require.Equal(t, 5, svc.lookups())
			require.Equal(t, before, rateLimiterGoroutines(t), "%d unauthenticated alert types must not start refill goroutines", probes)
			require.Zero(t, telegramRatelimiter.Stats().RetainedTitles, "%d unauthenticated alert types must not keep per-type state", probes)
		})
	}
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
// turning into one MongoDB lookup each while the credential directory cannot
// decide: a separate work budget bounds the lookup.
func TestAlertThrottleBoundsCredentialLookups(t *testing.T) {
	// The directory cannot decide; the independent work budget bounds the flood.
	configureAlertThrottle(t, 1, 1, 100, 100)
	svc := &tokenGatedTelegram{tokens: map[string]string{}, directoryErr: errors.New("synthetic directory outage")}
	resolver := NewMutationResolver(svc)
	resolver.credentials.fallback = rate.NewLimiter(0, 5)

	throttled := 0
	for i := range 50 {
		_, err := resolver.TelegramMonitorAlert(context.Background(), fmt.Sprintf("flood-%02d", i), "anonymous", "body")
		require.Error(t, err)
		if err.Error() == "deny by throttle" {
			throttled++
		}
	}

	require.Equal(t, 5, svc.authCalls)
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
