package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeHost covers every form site and Turnstile host matching used
// to normalize separately, so the shared rule keeps both behaviors.
func TestNormalizeHost(t *testing.T) {
	for input, want := range map[string]string{
		"SSO.LAISKY.COM:443":   "sso.laisky.com",
		" SSO.LAISKY.COM:443 ": "sso.laisky.com",
		"mcp.laisky.com":       "mcp.laisky.com",
		"mcp.laisky.com.":      "mcp.laisky.com",
		"mcp.laisky.com.:8443": "mcp.laisky.com",
		"127.0.0.1:8080":       "127.0.0.1",
		"[fd00::1]:8443":       "fd00::1",
		"[FD00::1]":            "fd00::1",
		"   ":                  "",
		".":                    "",
		"":                     "",
	} {
		require.Equal(t, want, NormalizeHost(input), "input %q", input)
	}
	require.Equal(t, []string{"a.example", "b.example"}, NormalizeHosts([]string{"A.example:80", " ", "b.example."}))
}

// TestRequestHostnameIgnoresUntrustedForwardedHost verifies the normalized
// request host only follows X-Forwarded-Host from a trusted proxy.
func TestRequestHostnameIgnoresUntrustedForwardedHost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://SSO.example.test:8443/", nil)
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	require.Equal(t, "sso.example.test", RequestHostname(r))
	require.Empty(t, RequestHostname(nil))
}
