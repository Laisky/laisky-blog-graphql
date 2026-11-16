package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// knownKeysCtx gives each simulated key its own client source.
func knownKeysCtx(key int) context.Context {
	return clientip.WithIP(context.Background(), fmt.Sprintf("198.18.%d.%d", key>>8, key&255))
}

// TestOutageKeepsEveryKnownKeyWithinGrace reproduces a finding of the
// independent review of e1ea009: during a one-api outage every known key
// retried upstream every 15 s, the budgets ran out, and the resulting local
// refusals denied stale grace to most keys. One authority failure now opens
// a process-wide outage window: known keys within grace are served without
// spending budget, and one probe per interval checks for recovery.
func TestOutageKeepsEveryKnownKeyWithinGrace(t *testing.T) {
	const keys = 100
	authority := &scriptedAuthority{verdict: map[string]error{}}
	for key := range keys {
		authority.verdict[fmt.Sprintf("sk-known-%d", key)] = nil
	}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	for key := range keys {
		_, err := authenticator.Authenticate(knownKeysCtx(key), fmt.Sprintf("Bearer sk-known-%d", key))
		require.NoError(t, err)
		clock.Advance(time.Second)
	}

	authority.outage.Store(true)
	before := authority.calls.Load()
	refused := 0
	for elapsed := time.Duration(0); elapsed < 10*time.Minute; elapsed += time.Second {
		for key := range keys {
			if int(elapsed/time.Second)%30 != key%30 {
				continue
			}
			if _, err := authenticator.Authenticate(knownKeysCtx(key), fmt.Sprintf("Bearer sk-known-%d", key)); err != nil {
				refused++
			}
		}
		clock.Advance(time.Second)
	}
	require.Zero(t, refused, "every key accepted before the outage stays within positive_ttl+stale_grace")
	require.LessOrEqual(t, authority.calls.Load()-before, int64(10*60/15+5), "about one probe per retry interval, not one per key")
}

// TestOutageEndsWithTheFirstAnsweredProbe: once one-api answers again, the
// outage window closes and a key revoked meanwhile is rejected.
func TestOutageEndsWithTheFirstAnsweredProbe(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-a": nil, "sk-revoked": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: time.Hour})
	for _, key := range []string{"sk-a", "sk-revoked"} {
		_, err := authenticator.Authenticate(context.Background(), "Bearer "+key)
		require.NoError(t, err)
	}
	clock.Advance(2 * time.Minute)
	authority.outage.Store(true)
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-a")
	require.NoError(t, err, "stale grace during the outage")

	authority.mu.Lock()
	authority.verdict["sk-revoked"] = ErrCredentialRejected
	authority.mu.Unlock()
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-revoked")
	require.NoError(t, err, "inside the outage window no probe is due yet")

	authority.outage.Store(false)
	clock.Advance(unavailableRetry)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-a")
	require.NoError(t, err, "the probe is answered")
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-revoked")
	require.ErrorIs(t, err, ErrCredentialRejected, "the outage is over, so the revocation is seen at once")
}

// TestKeySpecificFailureOpensNoOutage: one-api answers 500 for a token
// whose owner lookup fails, and 200 {"success":false} for some controller
// failures. Such an answer says nothing about other keys, so a caller who
// holds such a token cannot extend every revoked key's life.
func TestKeySpecificFailureOpensNoOutage(t *testing.T) {
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer sk-broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "Bearer sk-envelope":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":false}`))
		case "Bearer sk-victim":
			if revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	validator, err := NewOneAPITokenValidator(server.URL, nil)
	require.NoError(t, err)
	authenticator, clock := newScriptedAuthenticatorFor(t, validator, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: time.Hour})

	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-victim")
	require.NoError(t, err)
	clock.Advance(2 * time.Minute)
	revoked.Store(true)
	for _, key := range []string{"sk-broken", "sk-envelope"} {
		_, err = authenticator.Authenticate(knownKeysCtx(7), "Bearer "+key)
		require.True(t, IsUnavailable(err), "%v", err)
	}
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-victim")
	require.ErrorIs(t, err, ErrCredentialRejected, "a key-specific failure must not serve other keys stale")
}

// TestOutageWindowNeedsFreshEvidence: a probe refused by this server's own
// budget keeps serving stale only while the last real authority failure is
// recent; it can never keep the window open by itself.
func TestOutageWindowNeedsFreshEvidence(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-a": nil, "sk-b": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{
		// Three calls of budget: two onboardings and the failing probe.
		PositiveTTL: time.Minute, StaleGrace: time.Hour, UpstreamBurst: 3, UpstreamRatePerSecond: 0.001,
		PerSourceBurst: 3, PerSourceRatePerSecond: 0.001, RevalidationRatePerSecond: -1,
	})
	for _, key := range []string{"sk-a", "sk-b"} {
		_, err := authenticator.Authenticate(context.Background(), "Bearer "+key)
		require.NoError(t, err)
	}
	clock.Advance(2 * time.Minute)
	authority.outage.Store(true)
	_, err := authenticator.Authenticate(knownKeysCtx(1), "Bearer sk-a")
	require.NoError(t, err, "a real failure (no budget left afterwards) opens the window")

	clock.Advance(unavailableRetry)
	_, err = authenticator.Authenticate(knownKeysCtx(2), "Bearer sk-b")
	require.NoError(t, err, "the probe is refused locally, but the last authority failure is recent")

	clock.Advance(2 * unavailableRetry)
	_, err = authenticator.Authenticate(knownKeysCtx(3), "Bearer sk-b")
	require.ErrorIs(t, err, ErrUpstreamThrottled, "without fresh evidence a local refusal never grants stale grace")
}

// TestRefusedChargesNeverSpendTokens reproduces a finding of the independent
// review: golang.org/x/time/rate cannot refund a canceled reservation once a
// later one landed, so concurrent refused charges left the global budget in
// debt. Charges are serialized, so a refused call keeps every token.
func TestRefusedChargesNeverSpendTokens(t *testing.T) {
	ledger := oneapi.NewCallLedger(oneapi.RateLimitWindow)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	ledger.Record(now, oneapi.RouteTokenConsume)
	opts := normalizeOptions(AuthenticatorOptions{Ledger: ledger, OneAPIRateLimit: 1, Now: func() time.Time { return now }})
	for range 20 {
		gate := newUpstreamGate(opts)
		source := gate.sources.resolve(clientip.WithIP(context.Background(), "192.0.2.1"))
		var wg sync.WaitGroup
		for range 64 {
			wg.Go(func() {
				require.Equal(t, refusalWindow, gate.charge(source, callNewKey, now), "the window is full")
			})
		}
		wg.Wait()
		require.InDelta(t, float64(opts.UpstreamBurst), gate.global.TokensAt(now), 1e-9, "refused calls spent global tokens")
	}
}

// TestLateAcceptanceNeverUndoesANewerRejection reproduces a finding of the
// independent review: a validation and a balance call for one key can
// overlap; if the older call's acceptance lands after the newer call's
// rejection, the key must stay rejected.
func TestLateAcceptanceNeverUndoesANewerRejection(t *testing.T) {
	var calls atomic.Int64
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	validator, err := NewOneAPITokenValidator(server.URL, nil)
	require.NoError(t, err)
	authenticator, clock := newScriptedAuthenticatorFor(t, validator, AuthenticatorOptions{})

	done := make(chan error, 1)
	go func() {
		_, authErr := authenticator.Authenticate(context.Background(), "Bearer sk-revoked")
		done <- authErr
	}()
	<-firstStarted
	clock.Advance(time.Second)
	_, err = authenticator.Balance(context.Background(), "sk-revoked")
	require.ErrorIs(t, err, ErrCredentialRejected)
	close(releaseFirst)
	require.ErrorIs(t, <-done, ErrCredentialRejected, "the newer rejection wins over the older call's acceptance")

	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-revoked")
	require.ErrorIs(t, err, ErrCredentialRejected, "no cached acceptance survives")
	require.Equal(t, int64(2), calls.Load())
}

// TestAcceptanceIsDatedAtCallStart: the trust period runs from when the
// accepting call started, so the revocation lag is at most positive_ttl
// after one-api's last acceptance, however slow the call was.
func TestAcceptanceIsDatedAtCallStart(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-slow": nil}}
	var clockRef *fakeClock
	slow := ValidatorFunc(func(ctx context.Context, apiKey string) error {
		clockRef.Advance(4 * time.Second) // the call itself takes 4 s
		return authority.ValidateAPIKey(ctx, apiKey)
	})
	authenticator, clock := newScriptedAuthenticatorFor(t, slow, AuthenticatorOptions{PositiveTTL: time.Minute, Timeout: 10 * time.Second})
	clockRef = clock
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-slow")
	require.NoError(t, err)
	clock.Advance(time.Minute - 4*time.Second)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-slow")
	require.NoError(t, err)
	require.Equal(t, int64(2), authority.calls.Load(), "trust ended positive_ttl after the call started")
}

// TestCopiedContextPointedAtAnotherTenantIsUnverified reproduces a finding
// of the independent review: copying a verified Context kept its verified
// bit, so changing the copy's tenant fields forged a verified identity. A
// context whose identity fields change after verification is unverified.
func TestCopiedContextPointedAtAnotherTenantIsUnverified(t *testing.T) {
	authenticator, err := NewAuthenticator(InsecureAcceptAllValidator{}, UnmeteredOptions("test"))
	require.NoError(t, err)
	verified, err := authenticator.Authenticate(context.Background(), "Bearer sk-attacker")
	require.NoError(t, err)
	victim, err := DeriveFromAPIKey("sk-victim")
	require.NoError(t, err)

	forged := *verified
	forged.APIKeyHash, forged.UserID, forged.UserIdentity = victim.APIKeyHash, victim.UserID, victim.UserIdentity
	got, ok := FromContext(WithContext(context.Background(), &forged))
	require.False(t, ok, "a forged copy is no verified identity")
	require.Nil(t, got)

	copied := *verified
	got, ok = FromContext(WithContext(context.Background(), &copied))
	require.True(t, ok, "an unchanged copy stays verified")
	require.Equal(t, verified.APIKeyHash, got.APIKeyHash)
}

// TestBaseURLWithQueryOrFragmentIsRefused: one-api's base URL is a prefix of
// /api routes, so a query or fragment would end up before the route and
// silently send every validation to another page.
func TestBaseURLWithQueryOrFragmentIsRefused(t *testing.T) {
	for _, base := range []string{"https://oneapi.example.com/?region=eu", "https://oneapi.example.com/#x"} {
		settings := DefaultValidationSettings()
		settings.BaseURL = base
		require.Error(t, CheckValidationBounds(settings), base)
		_, err := NewOneAPITokenValidator(base, nil)
		require.Error(t, err, base)
	}
	legacy := DefaultValidationSettings()
	legacy.Endpoint = "https://proxy.example.com/balance?tenant=a"
	require.NoError(t, CheckValidationBounds(legacy), "the legacy endpoint stays verbatim, query included")
}
