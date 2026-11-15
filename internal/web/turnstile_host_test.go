package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// turnstileHostConfigs are the Turnstile layouts the host tests run against.
func turnstileHostConfigs() map[string]map[string]any {
	return map[string]map[string]any{
		// docs/example/config/settings.yml: only sso has a pair, nothing is default.
		"example config": {
			"settings.web.turnstile.secret_key": "",
			"settings.web.turnstile.site_key":   "",
			"settings.web.sites": map[string]any{
				"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp"},
				"sso": map[string]any{"host": "sso.laisky.com", "router": "sso", "turnstile_site_key": "sso-key", "turnstile_secret_key": "sso-secret"},
			},
		},
		// The production layout: sso is default and has the only pair.
		"production-like config": {
			"settings.web.turnstile.secret_key": "",
			"settings.web.turnstile.site_key":   "",
			"settings.web.sites": map[string]any{
				"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp"},
				"sso": map[string]any{"host": "sso.laisky.com", "router": "sso", "default": true, "turnstile_site_key": "sso-key", "turnstile_secret_key": "sso-secret"},
			},
		},
	}
}

// TestServerEngineHostCannotDisableChallenge reproduces review finding 1
// through the production engine: neither the Host header nor a spoofed or
// relayed X-Forwarded-Host may switch the Turnstile gate off. Each client must
// be challenged once it exceeds the frequency threshold (5 per minute).
func TestServerEngineHostCannotDisableChallenge(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		remote  string
		headers map[string]string
	}{
		{"own host", "sso.laisky.com", "198.51.100.201:1", nil},
		{"keyless alias host", "mcp.laisky.com", "198.51.100.202:1", nil},
		{"unknown host", "nope.invalid", "198.51.100.203:1", nil},
		{"direct peer spoofs X-Forwarded-Host", "sso.laisky.com", "198.51.100.204:1", map[string]string{"X-Forwarded-Host": "nope.invalid"}},
		{"trusted proxy relays a client X-Forwarded-Host", "sso.laisky.com", "100.64.0.10:1",
			map[string]string{"X-Forwarded-For": "198.51.100.205", "X-Forwarded-Host": "nope.invalid"}},
	}
	for configName, turnstile := range turnstileHostConfigs() {
		t.Run(configName, func(t *testing.T) {
			engine := newIdentityEngineWithTurnstile(t, nil, turnstile)
			for _, tc := range cases {
				var got []string
				for range 8 {
					got = append(got, startPasskeyLoginOn(t, engine, tc.host, tc.remote, tc.headers))
				}
				require.Equal(t, []string{"", "", "", "", ""}, got[:5], "%s: requests within the threshold are admitted", tc.name)
				for i, message := range got[5:] {
					require.Contains(t, message, "turnstile_required", "%s: request %d must be challenged", tc.name, i+6)
				}
			}
		})
	}
}

// TestServerEngineWithoutSecretsKeepsGateOff is the control: with no secret
// anywhere Turnstile stays disabled.
func TestServerEngineWithoutSecretsKeepsGateOff(t *testing.T) {
	engine := newIdentityEngineWithTurnstile(t, nil, map[string]any{
		"settings.web.turnstile.secret_key": "",
		"settings.web.turnstile.site_key":   "",
		"settings.web.sites": map[string]any{
			"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp"},
		},
	})
	for i := range 8 {
		require.Empty(t, startPasskeyLoginOn(t, engine, "mcp.laisky.com", "198.51.100.210:1", nil), "request %d", i+1)
	}
}

// TestRuntimeConfigServesTheVerifyingWidgetKey verifies the runtime config
// serves, for every host, the widget key of the pair the server verifies with,
// so the SSO page on a host without its own pair (mcp in production) can
// still render the challenge the gate demands.
func TestRuntimeConfigServesTheVerifyingWidgetKey(t *testing.T) {
	for configName, turnstile := range turnstileHostConfigs() {
		t.Run(configName, func(t *testing.T) {
			ssoGlobalsMu.Lock()
			t.Cleanup(ssoGlobalsMu.Unlock)
			restoreSSOConfig(t, turnstile)
			for _, tc := range []struct {
				host    string
				remote  string
				headers map[string]string
			}{
				{"sso.laisky.com", "198.51.100.1:1", nil},
				{"mcp.laisky.com", "198.51.100.1:1", nil},
				{"nope.invalid", "198.51.100.1:1", map[string]string{"X-Forwarded-Host": "mcp.laisky.com"}},
				{"mcp.laisky.com", "100.64.0.10:1", map[string]string{"X-Forwarded-Host": "sso.laisky.com"}},
			} {
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/runtime-config.json", tc.host), nil)
				req.RemoteAddr = tc.remote
				for name, value := range tc.headers {
					req.Header.Set(name, value)
				}
				site := withTurnstileSiteKey(SiteConfig{ID: "any", TurnstileSiteKey: "stale"}, throughIdentityPolicy(t, req))
				require.Equal(t, "sso-key", site.TurnstileSiteKey, "host %s", tc.host)
			}
		})
	}

	ssoGlobalsMu.Lock()
	t.Cleanup(ssoGlobalsMu.Unlock)
	restoreSSOConfig(t, map[string]any{
		"settings.web.turnstile.secret_key": "",
		"settings.web.sites":                map[string]any{"mcp": map[string]any{"host": "mcp.laisky.com", "turnstile_site_key": "lonely"}},
	})
	req := httptest.NewRequest(http.MethodGet, "http://mcp.laisky.com/runtime-config.json", nil)
	require.Empty(t, withTurnstileSiteKey(SiteConfig{TurnstileSiteKey: "lonely"}, req).TurnstileSiteKey,
		"a widget key without a secret is never served: no challenge can be verified")
}
