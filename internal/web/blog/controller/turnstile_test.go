package controller

import (
	"context"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestNormalizeTurnstileHost verifies host normalization removes ports and normalizes case.
func TestNormalizeTurnstileHost(t *testing.T) {
	require.Equal(t, "sso.laisky.com", normalizeTurnstileHost(" SSO.LAISKY.COM:443 "))
	require.Equal(t, "fd00::1", normalizeTurnstileHost("[fd00::1]:8443"))
	require.Equal(t, "127.0.0.1", normalizeTurnstileHost("127.0.0.1:8080"))
	require.Equal(t, "", normalizeTurnstileHost("   "))
}

// TestResolveTurnstileSecretForLogin verifies host-based secret selection from site settings.
func TestResolveTurnstileSecretForLogin(t *testing.T) {
	originalSites := gconfig.Shared.GetStringMap("settings.web.sites")
	originalGlobalSecret := gconfig.Shared.GetString("settings.web.turnstile.secret_key")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", originalSites)
		gconfig.Shared.Set("settings.web.turnstile.secret_key", originalGlobalSecret)
	})

	sites := map[string]any{
		"mcp": map[string]any{
			"host":                 "mcp.laisky.com",
			"turnstile_secret_key": "mcp-secret",
			"turnstile_site_key":   "mcp-site-key",
			"public_base_path":     "/",
			"router":               "mcp",
			"title":                "MCP",
		},
		"sso": map[string]any{
			"hosts":                []string{"sso.laisky.com"},
			"default":              true,
			"turnstile_secret_key": "sso-secret",
			"turnstile_site_key":   "sso-site-key",
			"public_base_path":     "/",
			"router":               "sso",
			"title":                "SSO",
		},
	}
	gconfig.Shared.Set("settings.web.sites", sites)
	gconfig.Shared.Set("settings.web.turnstile.secret_key", "global-secret")

	require.Equal(t, "sso-secret", resolveTurnstileSecretForLogin("sso.laisky.com"))
	require.Equal(t, "mcp-secret", resolveTurnstileSecretForLogin("mcp.laisky.com"))
	require.Equal(t, "sso-secret", resolveTurnstileSecretForLogin("unknown.laisky.com"))
}

// TestResolveTurnstileSecretGlobalFallback verifies global fallback is used when site-level settings are absent.
func TestResolveTurnstileSecretGlobalFallback(t *testing.T) {
	originalSites := gconfig.Shared.GetStringMap("settings.web.sites")
	originalGlobalSecret := gconfig.Shared.GetString("settings.web.turnstile.secret_key")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", originalSites)
		gconfig.Shared.Set("settings.web.turnstile.secret_key", originalGlobalSecret)
	})

	gconfig.Shared.Set("settings.web.sites", map[string]any{})
	gconfig.Shared.Set("settings.web.turnstile.secret_key", "global-secret")
	require.Equal(t, "global-secret", resolveTurnstileSecretForLogin("sso.laisky.com"))
}

// setTurnstileEnabledSite configures a Turnstile-enabled default site for the test.
func setTurnstileEnabledSite(t *testing.T) {
	t.Helper()

	originalSites := gconfig.Shared.GetStringMap("settings.web.sites")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", originalSites)
	})

	gconfig.Shared.Set("settings.web.sites", map[string]any{
		"sso": map[string]any{
			"default":              true,
			"turnstile_secret_key": "sso-secret",
		},
	})
}

// TestValidateTurnstileTokenSkippedForLowRiskLogin verifies a low-risk client is
// not challenged even when Turnstile is configured.
func TestValidateTurnstileTokenSkippedForLowRiskLogin(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, newAuthChallengeTracker(time.Minute, 5, 15*time.Minute, 3, 100))

	require.NoError(t, validateTurnstileTokenForLogin(context.Background(), nil))
}

// TestValidateTurnstileTokenRequiredForHighRiskLogin verifies a suspicious client
// is asked to solve a challenge when no token is supplied.
func TestValidateTurnstileTokenRequiredForHighRiskLogin(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, highRiskTrackerForUnknown())

	err := validateTurnstileTokenForLogin(context.Background(), nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrTurnstileRequired))
}

// TestValidateTurnstileTokenRequiredForHighRiskRegister verifies registration by a
// suspicious client is gated behind a challenge.
func TestValidateTurnstileTokenRequiredForHighRiskRegister(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, highRiskTrackerForUnknown())

	err := validateTurnstileTokenForRegister(context.Background(), nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrTurnstileRequired))
}

// TestValidateTurnstileTokenRequiredForFrequentLogin verifies frequent login
// attempts activate the Turnstile challenge.
func TestValidateTurnstileTokenRequiredForFrequentLogin(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, newAuthChallengeTracker(time.Minute, 1, 15*time.Minute, 100, 100))

	require.NoError(t, validateTurnstileTokenForLogin(context.Background(), nil))
	err := validateTurnstileTokenForLogin(context.Background(), nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrTurnstileRequired))
}

// TestValidateTurnstileTokenRequiredForFrequentRegister verifies frequent
// registration attempts activate the Turnstile challenge.
func TestValidateTurnstileTokenRequiredForFrequentRegister(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, newAuthChallengeTracker(time.Minute, 1, 15*time.Minute, 100, 100))

	require.NoError(t, validateTurnstileTokenForRegister(context.Background(), nil))
	err := validateTurnstileTokenForRegister(context.Background(), nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrTurnstileRequired))
}

// TestBlogLoginRejectedForHighRiskClient verifies the legacy login path enforces
// the challenge for suspicious clients (masked as invalid credentials since the
// legacy client cannot render a widget).
func TestBlogLoginRejectedForHighRiskClient(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, highRiskTrackerForUnknown())

	_, err := (&MutationResolver{}).BlogLogin(context.Background(), "user@example.com", "password")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid credentials")
}
