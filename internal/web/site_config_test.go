package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestNormalizeHost verifies host normalization strips ports and lowercases values.
func TestNormalizeHost(t *testing.T) {
	require.Equal(t, "sso.laisky.com", normalizeHost("SSO.LAISKY.COM:443"))
	require.Equal(t, "mcp.laisky.com", normalizeHost("mcp.laisky.com"))
	require.Equal(t, "127.0.0.1", normalizeHost("127.0.0.1:8080"))
}

// TestSiteConfigSetResolveHost verifies site resolution respects the Host and X-Forwarded-Host headers.
func TestSiteConfigSetResolveHost(t *testing.T) {
	oldSites := gconfig.Shared.GetStringMap("settings.web.sites")
	siteSettings := map[string]any{
		"mcp": map[string]any{
			"hosts":            []string{"mcp.laisky.com"},
			"title":            "Laisky MCP",
			"router":           "mcp",
			"public_base_path": "/",
		},
		"sso": map[string]any{
			"host":                 "sso.laisky.com",
			"title":                "Laisky SSO",
			"router":               "sso",
			"default":              true,
			"public_base_path":     "/",
			"turnstile_site_key":   "test-turnstile-site-key",
			"turnstile_secret_key": "test-turnstile-secret-key",
		},
	}
	gconfig.Shared.Set("settings.web.sites", siteSettings)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", oldSites)
	})

	prefix := urlPrefixConfig{internal: "/mcp", public: "/"}
	set := loadSiteConfigSet(log.Logger.Named("site_config_test"), prefix)

	req := httptest.NewRequest(http.MethodGet, "https://sso.laisky.com/", nil)
	req.Host = "sso.laisky.com"
	site := set.resolveForRequest(req)
	require.Equal(t, "sso", site.ID)
	require.Equal(t, "sso", site.Router)
	require.Equal(t, "test-turnstile-site-key", site.TurnstileSiteKey)

	req = httptest.NewRequest(http.MethodGet, "https://mcp.laisky.com/", nil)
	req.Host = "mcp.laisky.com:443"
	site = set.resolveForRequest(req)
	require.Equal(t, "mcp", site.ID)
	require.Equal(t, "mcp", site.Router)

	req = httptest.NewRequest(http.MethodGet, "https://unknown.laisky.com/", nil)
	req.Host = "unknown.laisky.com"
	site = set.resolveForRequest(req)
	require.Equal(t, "sso", site.ID)

	req = httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	req.Header.Set("X-Forwarded-Host", "mcp.laisky.com, proxy.local")
	site = set.resolveForRequest(req)
	require.Equal(t, "mcp", site.ID)
}

// TestSiteConfigSetResolvePath verifies path-based site resolution for IP access.
func TestSiteConfigSetResolvePath(t *testing.T) {
	oldSites := gconfig.Shared.GetStringMap("settings.web.sites")
	siteSettings := map[string]any{
		"mcp": map[string]any{
			"hosts":            []string{"mcp.laisky.com"},
			"title":            "Laisky MCP",
			"router":           "mcp",
			"public_base_path": "/mcp",
		},
		"sso": map[string]any{
			"host":             "sso.laisky.com",
			"title":            "Laisky SSO",
			"router":           "sso",
			"default":          true,
			"public_base_path": "/sso",
		},
	}
	gconfig.Shared.Set("settings.web.sites", siteSettings)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sites", oldSites)
	})

	prefix := urlPrefixConfig{internal: "/mcp", public: "/"}
	set := loadSiteConfigSet(log.Logger.Named("site_config_test"), prefix)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/mcp/", nil)
	req.Host = "127.0.0.1:5173"
	site := set.resolveForRequest(req)
	require.Equal(t, "mcp", site.ID)

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/sso/login", nil)
	req.Host = "127.0.0.1:5173"
	site = set.resolveForRequest(req)
	require.Equal(t, "sso", site.ID)

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runtime-config.json", nil)
	req.Host = "127.0.0.1:5173"
	req.Header.Set("Referer", "http://127.0.0.1/sso/")
	site = set.resolveForRequest(req)
	require.Equal(t, "sso", site.ID)
}
