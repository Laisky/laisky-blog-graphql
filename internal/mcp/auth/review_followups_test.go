package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// rv95: a caller holding a revoked key drains its own per-source budget with
// random bearers, which turns its revalidation into "unavailable" and makes the
// stale-if-error branch accept the revoked key for positive_ttl + stale_grace.
func TestRV95RevokedKeyOutlivesPositiveTTLBySelfDrainingSourceBudget(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-leaked": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	ctx := clientip.WithIP(context.Background(), "203.0.113.7")

	_, err := authenticator.Authenticate(ctx, "Bearer sk-leaked")
	require.NoError(t, err)

	// Owner revokes the key in one-api.
	authority.mu.Lock()
	authority.verdict["sk-leaked"] = ErrCredentialRejected
	authority.mu.Unlock()

	clock.Advance(6 * time.Minute) // past the 5m positive TTL; source bucket full again
	random := 0
	drain := func(n int) {
		for range n {
			random++
			_, _ = authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-random-%d", random))
		}
	}
	drain(5)

	accepted := 0
	for elapsed := time.Duration(0); elapsed < 28*time.Minute; elapsed += 10 * time.Second {
		drain(1) // spend the token refilled in the last 10s
		authCtx, err := authenticator.Authenticate(ctx, "Bearer sk-leaked")
		if err == nil && authCtx.Verified() {
			accepted++
		}
		clock.Advance(10 * time.Second)
	}
	t.Logf("revoked key accepted %d times over ~28 minutes after positive TTL; upstream calls=%d", accepted, authority.calls.Load())
	require.Zero(t, accepted, "a revoked key must be refused once positive_ttl has passed (documented revocation lag)")
}

// rv95: stale_grace: 0 is accepted by the settings loader (clamp low bound 0)
// but NewAuthenticator replaces it with the 30m default.
func TestRV95ZeroStaleGraceCannotBeConfigured(t *testing.T) {
	settings := LoadValidationSettings(func(key string) any {
		if key == validationConfigPrefix+"stale_grace" {
			return "0s"
		}
		return nil
	})
	require.Zero(t, settings.StaleGrace, "loader keeps an explicit 0")
	authenticator, err := NewAuthenticatorFromSettings(settings, nil)
	require.NoError(t, err)
	require.Zero(t, authenticator.opts.StaleGrace, "an explicit stale_grace of 0 must disable stale-if-error")
}

// rv95: one-api's /api/token/balance answers its own failures as HTTP 200 with
// {"success": false} (helper.RespondError), and its SPA NoRoute answers 200
// HTML; both are treated as an accepted key.
func TestRV95OneAPIFailureEnvelopeAndHTMLAreAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":false,"message":"record not found"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html></html>`))
	}))
	t.Cleanup(server.Close)

	envelope, err := NewOneAPITokenValidator(server.URL, nil)
	require.NoError(t, err)
	spa, err := NewOneAPITokenValidator(server.URL+"/console", nil)
	require.NoError(t, err)

	errEnvelope := envelope.ValidateAPIKey(context.Background(), "sk-anything")
	errSPA := spa.ValidateAPIKey(context.Background(), "sk-anything")
	t.Logf("success:false envelope -> %v; SPA html -> %v", errEnvelope, errSPA)
	require.Error(t, errEnvelope, "a {success:false} body must not accept the key")
	require.Error(t, errSPA, "an HTML page must not accept the key")
}

// rv95: under a sustained global-budget flood that always spends the refilled
// token first, an actively used, recently validated key is never re-validated
// and is refused once positive_ttl + stale_grace elapse.
func TestRV95SustainedFloodEventuallyLocksOutActiveKey(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-victim": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	victim := clientip.WithIP(context.Background(), "198.51.100.1")
	_, err := authenticator.Authenticate(victim, "Bearer sk-victim")
	require.NoError(t, err)

	n := 0
	for i := range 31 { // initial burst of the global budget
		_, _ = authenticator.Authenticate(clientip.WithIP(context.Background(), fmt.Sprintf("10.0.%d.%d", i/256, i%256)), fmt.Sprintf("Bearer sk-r-%d", n))
		n++
	}
	firstRefusal := time.Duration(0)
	for elapsed := time.Second; elapsed <= 40*time.Minute; elapsed += time.Second {
		clock.Advance(time.Second)
		_, _ = authenticator.Authenticate(clientip.WithIP(context.Background(), fmt.Sprintf("10.1.%d.%d", n/256%256, n%256)), fmt.Sprintf("Bearer sk-r-%d", n))
		n++
		if elapsed%(30*time.Second) == 0 {
			if _, err := authenticator.Authenticate(victim, "Bearer sk-victim"); err != nil && firstRefusal == 0 {
				firstRefusal = elapsed
			}
		}
	}
	t.Logf("active victim key first refused after %s of flood (attacker ~1 req/s)", firstRefusal)
	require.Zero(t, firstRefusal, "docs: keys validated recently keep working during such a flood")
}

// TestLocalBudgetRefusalNeverGrantsStaleGrace: when this server's own budgets
// (not one-api) refuse the revalidation of a known key, the key is refused,
// so draining a budget cannot extend a revoked key past its positive TTL.
func TestLocalBudgetRefusalNeverGrantsStaleGrace(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{
		UpstreamRatePerSecond: 0.001, UpstreamBurst: 2, RevalidationRatePerSecond: -1,
	})
	_, err := authenticator.Authenticate(clientip.WithIP(context.Background(), "192.0.2.1"), "Bearer sk-known")
	require.NoError(t, err)
	_, _ = authenticator.Authenticate(clientip.WithIP(context.Background(), "192.0.2.2"), "Bearer sk-drain")

	clock.Advance(6 * time.Minute)
	_, err = authenticator.Authenticate(clientip.WithIP(context.Background(), "192.0.2.1"), "Bearer sk-known")
	require.ErrorIs(t, err, ErrUpstreamThrottled)
	require.True(t, IsUnavailable(err), "a local refusal is still a retryable 503")
	require.Equal(t, int64(2), authority.calls.Load())
}

// TestZeroStaleGraceRefusesDuringOutage checks the configured behavior, not
// only the stored option: with stale_grace 0 an outage after the TTL refuses.
func TestZeroStaleGraceRefusesDuringOutage(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{StaleGrace: -1})
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.NoError(t, err)
	authority.outage.Store(true)
	clock.Advance(6 * time.Minute)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.True(t, IsUnavailable(err), "%v", err)
	require.NotErrorIs(t, err, ErrUpstreamThrottled, "this is a genuine outage, refused only because the grace is 0")

	unset := LoadValidationSettings(func(string) any { return nil })
	require.Equal(t, DefaultValidationSettings().StaleGrace, unset.StaleGrace, "unset keeps the default")
	revalidationOff := LoadValidationSettings(func(key string) any {
		return map[string]any{validationConfigPrefix + "revalidation_rate_per_second": 0}[key]
	})
	built, err := NewAuthenticatorFromSettings(revalidationOff, nil)
	require.NoError(t, err)
	require.Nil(t, built.gate.revalidation, "an explicit 0 disables the revalidation reserve")
}

// TestOutageRetriesOfOneKnownKeyAreSpaced spends at most one upstream call per
// retry interval on a known key while one-api is down.
func TestOutageRetriesOfOneKnownKeyAreSpaced(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.NoError(t, err)
	authority.outage.Store(true)
	clock.Advance(6 * time.Minute)
	for range 50 {
		authCtx, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
		require.NoError(t, err, "within the stale grace window")
		require.True(t, authCtx.Verified())
	}
	require.Equal(t, int64(2), authority.calls.Load(), "one failed revalidation, then stale answers without upstream calls")
	clock.Advance(unavailableRetry)
	_, _ = authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.Equal(t, int64(3), authority.calls.Load())
}

// TestBalanceSharesVerdictCachesAndGate covers the balance proxies: answers are
// reused per key, they share the validation verdicts, and they are charged to
// the caller's source budget.
func TestBalanceSharesVerdictCachesAndGate(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer sk-balance" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":11,"used_quota":2}}`))
	}))
	t.Cleanup(server.Close)
	validator, err := NewOneAPITokenValidator(server.URL, nil)
	require.NoError(t, err)
	clock := &fakeClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	authenticator, err := NewAuthenticator(validator, AuthenticatorOptions{Now: clock.Now, PerSourceBurst: 3, PerSourceRatePerSecond: 0.001})
	require.NoError(t, err)
	source := clientip.WithIP(context.Background(), "203.0.113.20")

	for range 5 {
		balance, err := authenticator.Balance(source, "sk-balance")
		require.NoError(t, err)
		require.InDelta(t, 11, balance.RemainQuota, 0)
	}
	require.Equal(t, int64(1), calls.Load(), "a balance answer is reused per key")
	authCtx, err := authenticator.Authenticate(source, "Bearer sk-balance")
	require.NoError(t, err, "the balance call also validated the key")
	require.True(t, authCtx.Verified())
	require.Equal(t, int64(1), calls.Load())

	for i := range 10 {
		_, err = authenticator.Balance(source, fmt.Sprintf("sk-junk-%d", i))
		require.Error(t, err)
	}
	require.Equal(t, int64(3), calls.Load(), "junk balance queries spend only the caller's source budget")
}

// TestOneAPIValidatorRequiresJSONContentType: a success-shaped body served as
// anything but application/json (e.g. a proxy or SPA page) is not a verdict.
func TestOneAPIValidatorRequiresJSONContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":1}}`))
	}))
	t.Cleanup(server.Close)
	validator, err := NewOneAPITokenValidator(server.URL, nil)
	require.NoError(t, err)
	err = validator.ValidateAPIKey(context.Background(), "sk-anything")
	require.ErrorIs(t, err, ErrValidationUnavailable)
	require.NotErrorIs(t, err, ErrCredentialRejected)
}
