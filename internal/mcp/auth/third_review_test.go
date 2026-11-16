package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCanceledWaiterNeverReceivesStaleGrace reproduces finding P3 (third
// maintainer review of PR #95): a caller whose own context ends while it
// waits for a shared validation is not an authority failure, so a key past
// its positive TTL must not be served from the stale-grace window.
func TestCanceledWaiterNeverReceivesStaleGrace(t *testing.T) {
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: time.Hour})
	_, err := authenticator.Authenticate(context.Background(), "Bearer sk-known")
	require.NoError(t, err)

	clock.Advance(2 * time.Minute) // past the TTL, inside the stale grace
	authority.gate = make(chan struct{})
	defer close(authority.gate)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	authCtx, err := authenticator.Authenticate(canceled, "Bearer sk-known")
	require.Nil(t, authCtx, "a canceled waiter is no evidence that one-api is down")
	require.True(t, IsUnavailable(err), "%v", err)
	require.Equal(t, UnavailableCauseLocal, UnavailableCause(err))
}

// TestOversizedKeyIsRefusedBeforeDerivation reproduces finding P3: the
// length bound applies before a key is hashed into tenant identifiers, on
// every path that derives them.
func TestOversizedKeyIsRefusedBeforeDerivation(t *testing.T) {
	oversized := "sk-" + strings.Repeat("k", MaxAPIKeyLength)

	derived, err := DeriveFromAPIKey(oversized)
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	require.Nil(t, derived, "no tenant identifiers exist for an oversized key")

	parsed, err := parseAuthorizationHeader("Bearer " + oversized)
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	require.Nil(t, parsed)

	parsed, err = parseAuthorizationHeader("Bearer workspace@" + oversized)
	require.ErrorIs(t, err, ErrInvalidAuthorization, "the legacy identity@token form is bounded too")
	require.Nil(t, parsed)

	exact := strings.Repeat("k", MaxAPIKeyLength)
	derived, err = DeriveFromAPIKey(exact)
	require.NoError(t, err, "a key of exactly the maximum length is accepted")
	require.Equal(t, exact, derived.APIKey)

	authority := &scriptedAuthority{verdict: map[string]error{}}
	authenticator, _ := newScriptedAuthenticator(t, authority, AuthenticatorOptions{})
	_, err = authenticator.Authenticate(context.Background(), "Bearer "+oversized)
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	_, err = authenticator.Balance(context.Background(), oversized)
	require.ErrorIs(t, err, ErrInvalidAuthorization)
	require.Zero(t, authority.calls.Load(), "an oversized key never reaches the authority")
}

// TestLegacyBalanceURLKeepsItsExactEndpoint reproduces finding P2: the
// pre-existing settings.oneapi.balance_url named an exact endpoint. A
// deployment whose proxy serves it under a custom path keeps working.
func TestLegacyBalanceURLKeepsItsExactEndpoint(t *testing.T) {
	var paths atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path)
		if r.URL.Path != "/custom/balance" || r.Header.Get("Authorization") != "Bearer sk-legacy" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":3}}`))
	}))
	t.Cleanup(server.Close)

	settings := LoadValidationSettings(func(key string) any {
		return map[string]any{legacyBalanceURLKey: server.URL + "/custom/balance"}[key]
	})
	authenticator, err := NewAuthenticatorFromSettings(settings, nil)
	require.NoError(t, err)

	authCtx, err := authenticator.Authenticate(context.Background(), "Bearer sk-legacy")
	require.NoError(t, err)
	require.True(t, authCtx.Verified())
	require.Equal(t, "/custom/balance", paths.Load(), "the legacy override is used verbatim")
	require.False(t, authenticator.ObservesAuthority(server.URL+"/custom/balance"),
		"a custom endpoint names no one-api base, so consumes cannot be matched to it")
}

// TestLegacyBalanceURLWithStandardPathKeepsConsumeRefresh: the common legacy
// value (an origin plus /api/token/balance) still names a one-api base, so
// consumes to that base keep refreshing verdicts.
func TestLegacyBalanceURLWithStandardPathKeepsConsumeRefresh(t *testing.T) {
	settings := LoadValidationSettings(func(key string) any {
		return map[string]any{legacyBalanceURLKey: "https://oneapi.example.com/prefix/api/token/balance"}[key]
	})
	authenticator, err := NewAuthenticatorFromSettings(settings, nil)
	require.NoError(t, err)
	validator, ok := authenticator.Validator().(*OneAPITokenValidator)
	require.True(t, ok)
	require.Equal(t, "https://oneapi.example.com/prefix/api/token/balance", validator.Endpoint())
	require.True(t, authenticator.ObservesAuthority("https://oneapi.example.com/prefix"))

	explicit := LoadValidationSettings(func(key string) any {
		return map[string]any{
			legacyBalanceURLKey:                 "https://legacy.example.com/custom/balance",
			validationConfigPrefix + "base_url": "https://oneapi.example.com",
		}[key]
	})
	authenticator, err = NewAuthenticatorFromSettings(explicit, nil)
	require.NoError(t, err)
	validator, ok = authenticator.Validator().(*OneAPITokenValidator)
	require.True(t, ok)
	require.Equal(t, "https://oneapi.example.com"+OneAPITokenBalancePath, validator.Endpoint(), "base_url wins over the legacy key")
}

// TestTenantHashIsSHA256OfTheTrimmedKey pins invariant I7 of
// docs/arch/mcp_auth.md: the tenant is the hex SHA-256 of the trimmed key,
// the scheme stored data was written under, and a legacy identity@token
// bearer is the same tenant as its token.
func TestTenantHashIsSHA256OfTheTrimmedKey(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-tenant"))
	want := hex.EncodeToString(sum[:])

	derived, err := DeriveFromAPIKey("  sk-tenant\t")
	require.NoError(t, err)
	require.Equal(t, want, derived.APIKeyHash)
	require.Equal(t, "user:"+want[:16], derived.UserID)

	legacy, err := parseAuthorizationHeader("Bearer workspace@sk-tenant")
	require.NoError(t, err)
	require.Equal(t, want, legacy.APIKeyHash)
}
