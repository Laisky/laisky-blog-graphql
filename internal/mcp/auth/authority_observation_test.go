package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

const observedBaseURL = "https://oneapi.example"

// originAuthority is a scripted authority that names its network origin.
type originAuthority struct {
	*scriptedAuthority
}

// AuthorityBaseURL reports the scripted authority's base URL in mixed case.
func (originAuthority) AuthorityBaseURL() string { return "https://OneAPI.Example/" }

// observedKeyAt returns an authenticator whose authority accepted sk-active
// at the start of the fake clock, plus its call counter.
func observedKeyAt(t *testing.T) (*Authenticator, *fakeClock, *scriptedAuthority) {
	t.Helper()
	authority := &scriptedAuthority{verdict: map[string]error{"sk-active": nil, "sk-unseen": nil}}
	clock := &fakeClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	authenticator, err := NewAuthenticator(originAuthority{authority}, AuthenticatorOptions{Now: clock.Now})
	require.NoError(t, err)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-active")
	require.NoError(t, err)
	require.Equal(t, int64(1), authority.calls.Load())
	return authenticator, clock, authority
}

// TestBeginAuthorityCallRestartsTrustOfAnAcceptedKey: an acceptance observed
// at t0+4m keeps the key served without a validation call until t0+9m, and
// the key needs the authority again positive_ttl after that acceptance.
func TestBeginAuthorityCallRestartsTrustOfAnAcceptedKey(t *testing.T) {
	authenticator, clock, authority := observedKeyAt(t)
	ctx := context.Background()

	clock.Advance(4 * time.Minute)
	authenticator.BeginAuthorityCall(observedBaseURL, "sk-active")(true)
	clock.Advance(4*time.Minute + 59*time.Second)
	_, err := authenticator.Authenticate(ctx, "Bearer sk-active")
	require.NoError(t, err)
	require.Equal(t, int64(1), authority.calls.Load(), "trusted from the observed acceptance")

	clock.Advance(time.Second)
	_, err = authenticator.Authenticate(ctx, "Bearer sk-active")
	require.NoError(t, err)
	require.Equal(t, int64(2), authority.calls.Load(), "positive_ttl after the last acceptance the authority decides again")
}

// TestBeginAuthorityCallChangesNothingElse pins what an observation must not
// do: trust another origin, act on a refusal, create a verdict for a key the
// validator never accepted, or move a newer acceptance backwards.
func TestBeginAuthorityCallChangesNothingElse(t *testing.T) {
	ctx := context.Background()
	for name, observe := range map[string]func(*Authenticator){
		"another host":         func(a *Authenticator) { a.BeginAuthorityCall("https://elsewhere.example", "sk-active")(true) },
		"another scheme":       func(a *Authenticator) { a.BeginAuthorityCall("http://oneapi.example", "sk-active")(true) },
		"a refused call":       func(a *Authenticator) { a.BeginAuthorityCall(observedBaseURL, "sk-active")(false) },
		"an unparseable key":   func(a *Authenticator) { a.BeginAuthorityCall(observedBaseURL, "")(true) },
		"an empty origin":      func(a *Authenticator) { a.BeginAuthorityCall("", "sk-active")(true) },
		"a relative origin":    func(a *Authenticator) { a.BeginAuthorityCall("/api/token/consume", "sk-active")(true) },
		"a non-http(s) scheme": func(a *Authenticator) { a.BeginAuthorityCall("ftp://oneapi.example", "sk-active")(true) },
	} {
		t.Run(name, func(t *testing.T) {
			authenticator, clock, authority := observedKeyAt(t)
			clock.Advance(4 * time.Minute)
			observe(authenticator)
			clock.Advance(2 * time.Minute)
			_, err := authenticator.Authenticate(ctx, "Bearer sk-active")
			require.NoError(t, err)
			require.Equal(t, int64(2), authority.calls.Load(), "the observation must not have refreshed trust")
		})
	}

	t.Run("an unknown key", func(t *testing.T) {
		authenticator, _, authority := observedKeyAt(t)
		authenticator.BeginAuthorityCall(observedBaseURL, "sk-unseen")(true)
		_, err := authenticator.Authenticate(ctx, "Bearer sk-unseen")
		require.NoError(t, err)
		require.Equal(t, int64(2), authority.calls.Load(), "only the validator creates a verdict")
	})

	t.Run("an older acceptance", func(t *testing.T) {
		authenticator, clock, authority := observedKeyAt(t)
		clock.Advance(time.Minute)
		older := authenticator.BeginAuthorityCall(observedBaseURL, "sk-active")
		clock.Advance(4*time.Minute + time.Second)
		_, err := authenticator.Authenticate(ctx, "Bearer sk-active") // revalidates at t0+5m1s
		require.NoError(t, err)
		require.Equal(t, int64(2), authority.calls.Load())
		older(true) // started at t0+1m, finished after the newer validation
		clock.Advance(4*time.Minute + 59*time.Second)
		_, err = authenticator.Authenticate(ctx, "Bearer sk-active")
		require.NoError(t, err)
		require.Equal(t, int64(2), authority.calls.Load(), "the newer validation still counts")
	})
}

// TestBeginAuthorityCallNeverResurrectsARejectedKey: a consume that started
// before the authority rejected the key and completes afterwards must not
// bring the key's trust back.
func TestBeginAuthorityCallNeverResurrectsARejectedKey(t *testing.T) {
	authenticator, clock, authority := observedKeyAt(t)
	ctx := context.Background()

	clock.Advance(time.Minute)
	inFlight := authenticator.BeginAuthorityCall(observedBaseURL, "sk-active")
	authority.mu.Lock()
	authority.verdict["sk-active"] = ErrCredentialRejected
	authority.mu.Unlock()
	clock.Advance(4*time.Minute + time.Second)
	_, err := authenticator.Authenticate(ctx, "Bearer sk-active")
	require.ErrorIs(t, err, ErrCredentialRejected)

	inFlight(true)
	_, err = authenticator.Authenticate(ctx, "Bearer sk-active")
	require.ErrorIs(t, err, ErrCredentialRejected)
	clock.Advance(31 * time.Second) // past negative_ttl
	_, err = authenticator.Authenticate(ctx, "Bearer sk-active")
	require.ErrorIs(t, err, ErrCredentialRejected, "the authority decides again; the late acceptance is ignored")
	require.Equal(t, int64(3), authority.calls.Load())
}

// TestBeginAuthorityCallIsSafeUnderConcurrentVerdicts runs observations,
// validations and rejections concurrently for the race detector.
func TestBeginAuthorityCallIsSafeUnderConcurrentVerdicts(t *testing.T) {
	authenticator, clock, _ := observedKeyAt(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				authenticator.BeginAuthorityCall(observedBaseURL, "sk-active")(true)
				_, _ = authenticator.Authenticate(context.Background(), "Bearer sk-active")
				clock.Advance(time.Second)
			}
		})
	}
	wg.Wait()
}

// TestOneAPIAuthenticatorObservesTheBillingOrigin wires the defaults: the
// one-api validator built from settings names oneapi.BillingAPI, so consumes
// of the production billing client refresh verdicts; validators without a
// network authority observe nothing.
func TestOneAPIAuthenticatorObservesTheBillingOrigin(t *testing.T) {
	authenticator, err := NewAuthenticatorFromSettings(DefaultValidationSettings(), nil)
	require.NoError(t, err)
	require.True(t, authenticator.ObservesAuthority(oneapi.BillingAPI))
	require.True(t, authenticator.ObservesAuthority(oneapi.BillingAPI+"/"))
	require.False(t, authenticator.ObservesAuthority(oneapi.BillingAPI+"/api"))
	require.False(t, authenticator.ObservesAuthority("https://elsewhere.example"))

	for _, baseURL := range []string{"https://oneapi.self-hosted.example", oneapi.BillingAPI + "/proxy"} {
		selfHosted := DefaultValidationSettings()
		selfHosted.BaseURL = baseURL
		authenticator, err = NewAuthenticatorFromSettings(selfHosted, nil)
		require.NoError(t, err)
		require.False(t, authenticator.ObservesAuthority(oneapi.BillingAPI), "consumes to another one-api prove nothing here: %s", baseURL)
		require.True(t, authenticator.ObservesAuthority(baseURL))
	}

	acceptAll, err := NewAuthenticator(InsecureAcceptAllValidator{}, UnmeteredOptions("accept-all"))
	require.NoError(t, err)
	require.False(t, acceptAll.ObservesAuthority(oneapi.BillingAPI))
	var missing *Authenticator
	require.False(t, missing.ObservesAuthority(oneapi.BillingAPI))
	missing.BeginAuthorityCall(oneapi.BillingAPI, "sk-active")(true)
}

// TestUnavailableCauseSeparatesAuthorityFromLocalFailures keeps the logged
// cause truthful: "authority" only when one-api was consulted and gave no
// verdict (or did moments ago for the key), "local_budget" for this server's
// budgets, and "local" for a request that ended while waiting or a missing
// authenticator.
func TestUnavailableCauseSeparatesAuthorityFromLocalFailures(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	ctx := context.Background()
	_, err := authenticator.Authenticate(ctx, "Bearer sk-known")
	require.NoError(t, err)

	authority.outage.Store(true)
	clock.Advance(5*time.Minute + time.Second)
	_, err = authenticator.Authenticate(ctx, "Bearer sk-unknown")
	require.Equal(t, UnavailableCauseAuthority, UnavailableCause(err), "%v", err)
	_, err = authenticator.Authenticate(ctx, "Bearer sk-other-unknown")
	require.Equal(t, UnavailableCauseAuthority, UnavailableCause(err))

	ended, cancel := context.WithCancel(ctx)
	cancel()
	gated := &scriptedAuthority{gate: make(chan struct{}), verdict: map[string]error{}}
	waiting, _ := newScriptedAuthenticator(t, gated, AuthenticatorOptions{})
	go func() { _, _ = waiting.Authenticate(ctx, "Bearer sk-shared") }()
	require.Eventually(t, func() bool { return gated.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
	_, err = waiting.Authenticate(ended, "Bearer sk-shared")
	require.Equal(t, UnavailableCauseLocal, UnavailableCause(err), "%v", err)
	close(gated.gate)

	var missing *Authenticator
	_, err = missing.Authenticate(ctx, "Bearer sk-known")
	require.Equal(t, UnavailableCauseLocal, UnavailableCause(err))

	require.Equal(t, UnavailableCauseLocalBudget, UnavailableCause(throttled(nil, "budget spent")))
}
