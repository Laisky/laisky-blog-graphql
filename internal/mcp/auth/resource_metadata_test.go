package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParsePublicOriginAcceptsOnlyBareOrigins pins the canonical form and the
// refusals: the metadata URL derives from configuration alone.
func TestParsePublicOriginAcceptsOnlyBareOrigins(t *testing.T) {
	for raw, want := range map[string]string{
		"https://mcp.laisky.com":           "https://mcp.laisky.com",
		" HTTPS://MCP.Example.Test:8443/ ": "https://mcp.example.test:8443",
		"http://100.75.198.70:17800":       "http://100.75.198.70:17800",
		"https://[2001:db8::1]:8443":       "https://[2001:db8::1]:8443",
	} {
		got, err := ParsePublicOrigin(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"", "mcp.laisky.com", "//mcp.laisky.com", "ftp://mcp.laisky.com", "https://", "https://:443",
		"https://u:p@mcp.laisky.com", "https://mcp.laisky.com/mcp", "https://mcp.laisky.com/?x=1", "https://mcp.laisky.com?",
		"https://mcp.laisky.com/#frag"} {
		_, err := ParsePublicOrigin(raw)
		require.ErrorContains(t, err, PublicOriginConfigKey, raw)
	}
}

// TestLoadPublicOriginDefaultsAndDerivesTheMetadataURL keeps the primary
// deployment's challenge unchanged when nothing is configured.
func TestLoadPublicOriginDefaultsAndDerivesTheMetadataURL(t *testing.T) {
	origin, err := LoadPublicOrigin(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultPublicOrigin, origin)
	require.Equal(t, "https://mcp.laisky.com/.well-known/oauth-protected-resource", ResourceMetadataURL(origin))
	require.Equal(t, DefaultResourceMetadataURL, ResourceMetadataURL(origin))

	origin, err = LoadPublicOrigin(func(string) any { return "https://mcp.example.test/" })
	require.NoError(t, err)
	require.Equal(t, "https://mcp.example.test/.well-known/oauth-protected-resource", ResourceMetadataURL(origin))

	_, err = LoadPublicOrigin(func(string) any { return []string{"https://mcp.example.test"} })
	require.ErrorContains(t, err, "must be a string")
}
