package controller

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
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

// TestValidateTurnstileTokenForLoginMissingToken verifies missing token is rejected when turnstile is enabled.
func TestValidateTurnstileTokenForLoginMissingToken(t *testing.T) {
	originalSites := gconfig.Shared.GetStringMap("settings.web.sites")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", originalSites)
	})

	sites := map[string]any{
		"sso": map[string]any{
			"default":              true,
			"turnstile_secret_key": "sso-secret",
		},
	}
	gconfig.Shared.Set("settings.web.sites", sites)

	err := validateTurnstileTokenForLogin(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "turnstile token is required")
}
