package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// validateTurnstileTokenForLogin runs the login gate once and settles the
// attempt, the view of a request that then does no credential check.
func validateTurnstileTokenForLogin(ctx context.Context, turnstileToken *string, account string) error {
	attempt, err := admitAuthRequest(ctx, turnstileToken, loginFlow(account))
	if err == nil {
		attempt.finish()
	}
	return err
}

// validateTurnstileTokenForRegister runs the registration gate once.
func validateTurnstileTokenForRegister(ctx context.Context, turnstileToken *string) error {
	attempt, err := admitAuthRequest(ctx, turnstileToken, registerFlow())
	if err == nil {
		attempt.finish()
	}
	return err
}

// resolveTurnstileSecretForLogin returns the secret of the pair selected for requestHost.
func resolveTurnstileSecretForLogin(requestHost string) string {
	return resolveTurnstileSite(requestHost).secret
}

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
			"turnstile_site_key":   "sso-site-key",
			"turnstile_secret_key": "sso-secret",
		},
	})
}

// TestValidateTurnstileTokenSkippedForLowRiskLogin verifies a low-risk client is
// not challenged even when Turnstile is configured.
func TestValidateTurnstileTokenSkippedForLowRiskLogin(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, newAuthChallengeTracker(time.Minute, 5, 15*time.Minute, 3, 100))

	require.NoError(t, validateTurnstileTokenForLogin(context.Background(), nil, ""))
}

// TestValidateTurnstileTokenRequiredForHighRiskLogin verifies a suspicious client
// is asked to solve a challenge when no token is supplied.
func TestValidateTurnstileTokenRequiredForHighRiskLogin(t *testing.T) {
	setTurnstileEnabledSite(t)
	withAuthChallengeTracker(t, highRiskTrackerForUnknown())

	err := validateTurnstileTokenForLogin(context.Background(), nil, "")
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

	require.NoError(t, validateTurnstileTokenForLogin(context.Background(), nil, ""))
	err := validateTurnstileTokenForLogin(context.Background(), nil, "")
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

// TestValidateTurnstileTokenAccountBudgetAcrossAddresses verifies the login
// gate challenges a request for an account that accumulated failures from
// other addresses, while account-less flows from the same address are not.
func TestValidateTurnstileTokenAccountBudgetAcrossAddresses(t *testing.T) {
	setTurnstileEnabledSite(t)
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	withAuthChallengeTracker(t, tracker)

	for i := range 3 {
		ctx := clientip.WithIP(context.Background(), fmt.Sprintf("203.0.113.%d", i+1))
		require.NoError(t, validateTurnstileTokenForLogin(ctx, nil, "victim@example.test"))
		tracker.recordFailure(resolveAuthRiskSubject(ctx, tracker, "victim@example.test"))
	}

	fresh := clientip.WithIP(context.Background(), "203.0.113.99")
	err := validateTurnstileTokenForLogin(fresh, nil, " Victim@Example.test")
	require.ErrorIs(t, err, model.ErrTurnstileRequired)
	require.NoError(t, validateTurnstileTokenForLogin(fresh, nil, ""), "passkey and GitHub starts name no account")
	require.NoError(t, validateTurnstileTokenForRegister(fresh, nil), "registration names no attacked account")
}

// TestResolveAuthClientKeyUsesSharedIdentity verifies the risk key comes from
// the shared client-identity rule, groups IPv6 by /64 and buckets unknowns.
func TestResolveAuthClientKeyUsesSharedIdentity(t *testing.T) {
	require.Equal(t, "203.0.113.5", resolveAuthClientKey(clientip.WithIP(context.Background(), "203.0.113.5")))
	require.Equal(t, "2001:db8:1:2::/64", resolveAuthClientKey(clientip.WithIP(context.Background(), "2001:db8:1:2::77")))
	require.Equal(t, unknownAuthClientKey, resolveAuthClientKey(context.Background()))
	require.Equal(t, "2001:db8:1:2::77", resolveTurnstileClientIP(clientip.WithIP(context.Background(), "2001:db8:1:2::77")),
		"siteverify receives the full address")
}
