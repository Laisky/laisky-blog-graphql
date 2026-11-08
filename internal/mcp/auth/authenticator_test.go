package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// fakeClock is a manually advanced clock for cache expiry tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the current fake time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake time forward.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// scriptedAuthority answers validations from a mutable verdict per key.
type scriptedAuthority struct {
	calls   atomic.Int64
	mu      sync.Mutex
	verdict map[string]error
	outage  atomic.Bool
	gate    chan struct{}
}

// ValidateAPIKey returns the scripted verdict and counts the call.
func (a *scriptedAuthority) ValidateAPIKey(ctx context.Context, apiKey string) error {
	a.calls.Add(1)
	if a.gate != nil {
		select {
		case <-a.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.outage.Load() {
		return errors.New("authority connection refused")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err, ok := a.verdict[apiKey]; ok {
		return err
	}
	return ErrCredentialRejected
}

// newScriptedAuthenticator builds an authenticator over a scripted authority and fake clock.
func newScriptedAuthenticator(t *testing.T, authority *scriptedAuthority, opts AuthenticatorOptions) (*Authenticator, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}
	opts.Now = clock.Now
	authenticator, err := NewAuthenticator(authority, opts)
	require.NoError(t, err)
	return authenticator, clock
}

func TestAuthenticatorCachesAcceptedKeyForPositiveTTL(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-issued": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute})

	for range 5 {
		authCtx, err := authenticator.Authenticate(context.Background(), "Bearer sk-issued")
		require.NoError(t, err)
		require.True(t, authCtx.Verified())
		require.Equal(t, "sk-issued", authCtx.APIKey)
	}
	require.Equal(t, int64(1), authority.calls.Load(), "an accepted key is validated once per TTL")

	clock.Advance(time.Minute)
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-issued")
	require.NoError(t, err)
	require.Equal(t, int64(2), authority.calls.Load(), "the key is revalidated after the TTL")
}

func TestAuthenticatorRejectsAndNegativelyCachesUnknownKey(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{NegativeTTL: 30 * time.Second})

	for range 3 {
		authCtx, err := authenticator.Authenticate(context.Background(), "Bearer sk-random")
		require.ErrorIs(t, err, ErrCredentialRejected)
		require.Nil(t, authCtx)
	}
	require.Equal(t, int64(1), authority.calls.Load())

	clock.Advance(30 * time.Second)
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-random")
	require.ErrorIs(t, err, ErrCredentialRejected)
	require.Equal(t, int64(2), authority.calls.Load(), "a rejection is not cached beyond its TTL")
}

func TestAuthenticatorHonorsRevocationAfterPositiveTTL(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-revoked-later": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: time.Hour})
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-revoked-later")
	require.NoError(t, err)

	authority.mu.Lock()
	authority.verdict["sk-revoked-later"] = ErrCredentialRejected
	authority.mu.Unlock()
	clock.Advance(time.Minute)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-revoked-later")
	require.ErrorIs(t, err, ErrCredentialRejected, "a definitive refusal overrides the stale grace window")
	authority.outage.Store(true)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-revoked-later")
	require.ErrorIs(t, err, ErrCredentialRejected, "a revoked key cannot be resurrected by a later outage")
}

func TestAuthenticatorCoalescesConcurrentValidations(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-burst": nil}, gate: make(chan struct{})}
	authenticator, _ := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			_, err := authenticator.Authenticate(context.Background(), "Bearer sk-burst")
			errs <- err
		})
	}
	require.Eventually(t, func() bool { return authority.calls.Load() == 1 }, time.Second, time.Millisecond)
	close(authority.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), authority.calls.Load(), "concurrent lookups of one key share one upstream call")
}

func TestAuthenticatorBoundsUpstreamCallsUnderRandomBearerFlood(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{}}
	authenticator, _ := newScriptedAuthenticator(t, authority, AuthenticatorOptions{UpstreamRatePerSecond: 0.001, UpstreamBurst: 5})

	rejected, unavailable := 0, 0
	for i := range 500 {
		_, err := authenticator.Authenticate(context.Background(), fmt.Sprintf("Bearer sk-flood-%04d", i))
		switch {
		case errors.Is(err, ErrCredentialRejected):
			rejected++
		case IsUnavailable(err):
			unavailable++
		default:
			t.Fatalf("a random bearer was accepted or misclassified: %v", err)
		}
	}
	require.LessOrEqual(t, authority.calls.Load(), int64(5), "a flood must not amplify into upstream calls")
	require.Equal(t, 5, rejected)
	require.Equal(t, 495, unavailable, "keys beyond the upstream budget fail closed, never open")
}

func TestAuthenticatorStaleIfErrorOnlyForRecentlyAcceptedKeys(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil, "sk-new": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: 10 * time.Minute})
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.NoError(t, err)

	authority.outage.Store(true)
	clock.Advance(5 * time.Minute)
	authCtx, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.NoError(t, err, "a recently accepted key survives an authority outage")
	require.True(t, authCtx.Verified())

	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-new")
	require.True(t, IsUnavailable(err), "a never-validated key fails closed during an outage: %v", err)
	require.NotErrorIs(t, err, ErrCredentialRejected)

	clock.Advance(10 * time.Minute)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.True(t, IsUnavailable(err), "the grace window is bounded: %v", err)
}

func TestAuthenticatorRejectsMalformedAndOversizedKeysWithoutUpstream(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{}}
	authenticator, _ := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})

	_, err := authenticator.Authenticate(context.Background(), "")
	require.ErrorIs(t, err, ErrMissingAuthorization)
	_, err = authenticator.Authenticate(context.Background(), "Bearer   ")
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	_, err = authenticator.Authenticate(context.Background(), "Bearer sk-"+strings.Repeat("a", MaxAPIKeyLength))
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	require.Zero(t, authority.calls.Load())
}

func TestUninstalledAuthenticatorFailsClosed(t *testing.T) {
	previous := Installed()
	Install(nil)
	t.Cleanup(func() { Install(previous) })

	_, err := Authenticate(context.Background(), "Bearer sk-anything")
	require.True(t, IsUnavailable(err), "no authenticator means no tenant: %v", err)
	_, err = FromContextOrHeader(context.Background(), "Bearer sk-anything")
	require.True(t, IsUnavailable(err))
}

func TestFromContextIgnoresUnverifiedContexts(t *testing.T) {
	derived, err := DeriveFromAPIKey("sk-self-asserted")
	require.NoError(t, err)
	_, ok := FromContext(WithContext(context.Background(), derived))
	require.False(t, ok, "a context the authenticator did not verify must never yield a tenant")

	authority := &scriptedAuthority{verdict: map[string]error{"sk-self-asserted": nil}}
	authenticator, _ := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	verified, err := authenticator.Authenticate(context.Background(), "Bearer sk-self-asserted")
	require.NoError(t, err)
	got, ok := FromContext(WithContext(context.Background(), verified))
	require.True(t, ok)
	require.Equal(t, derived.APIKeyHash, got.APIKeyHash, "validation keeps the existing tenant derivation")
	require.Equal(t, derived.UserID, got.UserID)
}

func TestOneAPITokenValidatorClassifiesAuthorityStatuses(t *testing.T) {
	statuses := map[string]int{
		"sk-ok": http.StatusOK, "sk-unknown": http.StatusUnauthorized, "sk-subnet": http.StatusForbidden,
		"sk-throttled": http.StatusTooManyRequests, "sk-broken": http.StatusInternalServerError, "sk-moved": http.StatusFound,
	}
	var paths sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.Method+" "+r.URL.Path, true)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if statuses[key] == http.StatusFound {
			http.Redirect(w, r, "https://attacker.invalid/collect", http.StatusFound)
			return
		}
		w.WriteHeader(statuses[key])
	}))
	t.Cleanup(server.Close)
	validator, err := NewOneAPITokenValidator(server.URL+"/", nil)
	require.NoError(t, err)
	require.Equal(t, server.URL+OneAPITokenBalancePath, validator.Endpoint())

	require.NoError(t, validator.ValidateAPIKey(context.Background(), "sk-ok"))
	for _, key := range []string{"sk-unknown", "sk-subnet"} {
		require.ErrorIs(t, validator.ValidateAPIKey(context.Background(), key), ErrCredentialRejected, key)
	}
	for _, key := range []string{"sk-throttled", "sk-broken", "sk-moved"} {
		err := validator.ValidateAPIKey(context.Background(), key)
		require.ErrorIs(t, err, ErrValidationUnavailable, key)
		require.NotErrorIs(t, err, ErrCredentialRejected, key)
	}
	_, onlyBalance := paths.Load("GET " + OneAPITokenBalancePath)
	require.True(t, onlyBalance, "validation must use the read-only balance route, never consume")
	paths.Range(func(key, _ any) bool {
		require.Equal(t, "GET "+OneAPITokenBalancePath, key)
		return true
	})

	server.Close()
	require.ErrorIs(t, validator.ValidateAPIKey(context.Background(), "sk-ok"), ErrValidationUnavailable, "a transport failure is not a rejection")
	_, err = NewOneAPITokenValidator("ftp://oneapi.invalid", nil)
	require.Error(t, err)
}

func TestValidationSettingsDefaultsAndClamps(t *testing.T) {
	defaults := LoadValidationSettings(func(string) any { return nil })
	require.Equal(t, DefaultValidationSettings(), defaults)
	require.True(t, defaults.Enabled, "production validation is on unless explicitly disabled")

	values := map[string]any{
		"settings.mcp.auth.validation.enabled":                  "false",
		"settings.mcp.auth.validation.positive_ttl":             "100h",
		"settings.mcp.auth.validation.negative_ttl":             "1h",
		"settings.mcp.auth.validation.timeout":                  "1ms",
		"settings.mcp.auth.validation.max_entries":              -5,
		"settings.mcp.auth.validation.upstream_rate_per_second": 0,
	}
	loaded := LoadValidationSettings(func(key string) any { return values[key] })
	require.False(t, loaded.Enabled)
	require.Equal(t, maxPositiveTTL, loaded.PositiveTTL)
	require.Equal(t, maxNegativeTTL, loaded.NegativeTTL)
	require.Equal(t, minTimeout, loaded.Timeout)
	require.Equal(t, 1, loaded.MaxEntries)
	require.InDelta(t, DefaultValidationSettings().UpstreamRatePerSecond, loaded.UpstreamRatePerSecond, 0)
}

func TestNewAuthenticatorFromSettingsSelectsOneAPIByDefault(t *testing.T) {
	authenticator, err := NewAuthenticatorFromSettings(DefaultValidationSettings(), nil)
	require.NoError(t, err)
	require.Equal(t, "oneapi", authenticator.ValidatorName())
	validator, ok := authenticator.Validator().(*OneAPITokenValidator)
	require.True(t, ok)
	require.Equal(t, strings.TrimSuffix(oneapi.BillingAPI, "/")+OneAPITokenBalancePath, validator.Endpoint())

	disabled := DefaultValidationSettings()
	disabled.Enabled = false
	authenticator, err = NewAuthenticatorFromSettings(disabled, nil)
	require.NoError(t, err)
	require.Equal(t, "disabled", authenticator.ValidatorName())
	require.IsType(t, InsecureAcceptAllValidator{}, authenticator.Validator())
}

func TestTTLCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newTTLCache[int](2)
	now := time.Now()
	cache.put("a", 1, now.Add(time.Hour))
	cache.put("b", 2, now.Add(time.Hour))
	_, ok := cache.get("a", now)
	require.True(t, ok)
	cache.put("c", 3, now.Add(time.Hour))
	_, ok = cache.get("c", now)
	require.True(t, ok)
	_, ok = cache.get("b", now)
	require.False(t, ok, "the least recently used entry is evicted at the bound")
	_, ok = cache.get("a", now.Add(2*time.Hour))
	require.False(t, ok, "expired entries are never served")
}
