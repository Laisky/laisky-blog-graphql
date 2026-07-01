package controller

import (
	"net/url"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/stretchr/testify/require"
)

// TestGitHubOAuthStateRoundTrip verifies signed OAuth state can be decoded.
func TestGitHubOAuthStateRoundTrip(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
	})
	gconfig.Shared.Set("settings.secret", "test-secret")

	state, err := signGitHubOAuthState(githubOAuthState{
		RedirectTo:  "https://app.laisky.com/callback",
		CallbackURL: "https://sso.laisky.com/github/callback",
		Nonce:       "nonce",
		ExpiresAt:   gutils.Clock.GetUTCNow().Add(githubOAuthStateTTL).Unix(),
	})
	require.NoError(t, err)

	decoded, err := verifyGitHubOAuthState(state)
	require.NoError(t, err)
	require.Equal(t, "https://app.laisky.com/callback", decoded.RedirectTo)
	require.Equal(t, "https://sso.laisky.com/github/callback", decoded.CallbackURL)
	require.Equal(t, "nonce", decoded.Nonce)
}

// TestGitHubOAuthStateRejectsTampering verifies state signatures are checked with constant-time comparison.
func TestGitHubOAuthStateRejectsTampering(t *testing.T) {
	originalSecret := gconfig.Shared.GetString("settings.secret")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", originalSecret)
	})
	gconfig.Shared.Set("settings.secret", "test-secret")

	state, err := signGitHubOAuthState(githubOAuthState{
		RedirectTo:  "https://app.laisky.com/callback",
		CallbackURL: "https://sso.laisky.com/github/callback",
		Nonce:       "nonce",
		ExpiresAt:   gutils.Clock.GetUTCNow().Add(githubOAuthStateTTL).Unix(),
	})
	require.NoError(t, err)

	_, err = verifyGitHubOAuthState(state + "tampered")
	require.Error(t, err)
}

// TestIsAllowedSSORedirectURL verifies redirect target allow-list behavior.
func TestIsAllowedSSORedirectURL(t *testing.T) {
	require.True(t, isAllowedSSORedirectURL(mustParseURL(t, "https://app.laisky.com/callback")))
	require.True(t, isAllowedSSORedirectURL(mustParseURL(t, "http://10.20.30.40/callback")))
	require.True(t, isAllowedSSORedirectURL(mustParseURL(t, "http://100.75.198.70/callback")))
	require.False(t, isAllowedSSORedirectURL(mustParseURL(t, "https://laisky.com.evil.com/callback")))
	require.False(t, isAllowedSSORedirectURL(mustParseURL(t, "javascript:alert(1)")))
}

// mustParseURL parses a URL string for tests.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}
