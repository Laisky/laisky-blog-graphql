package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateStartupConfigRefusesDisabledValidationInProdMode keeps the
// accept-all opt-out a development tool: outside --debug, a configuration
// that disables API key validation would let any bearer become a tenant, so
// startup fails instead of logging a warning.
func TestValidateStartupConfigRefusesDisabledValidationInProdMode(t *testing.T) {
	disabled := map[string]any{"settings": map[string]any{"mcp": map[string]any{"auth": map[string]any{
		"validation": map[string]any{"enabled": false},
	}}}}
	err := validateStartupConfigWithGetter(newMapConfigGetter(disabled))
	require.ErrorContains(t, err, "settings.mcp.auth.validation.enabled")

	disabled["debug"] = true
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(disabled)), "--debug keeps the development opt-out")

	enabled := map[string]any{"settings": map[string]any{"mcp": map[string]any{"auth": map[string]any{
		"validation": map[string]any{"enabled": true},
	}}}}
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(enabled)))
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(map[string]any{})), "validation is enabled by default")
}

// TestValidateStartupConfigChecksMCPPublicOrigin verifies the configured
// public origin, from which Bearer challenges derive the RFC 9728 metadata
// URL, is a bare http(s) origin.
func TestValidateStartupConfigChecksMCPPublicOrigin(t *testing.T) {
	withOrigin := func(origin any) map[string]any {
		return map[string]any{"settings": map[string]any{"mcp": map[string]any{"public_origin": origin}}}
	}
	for _, origin := range []any{"https://mcp.example.test", "https://MCP.example.test:8443/", "http://100.75.198.70:17800", ""} {
		require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(withOrigin(origin))), origin)
	}
	for _, origin := range []any{"mcp.example.test", "ftp://mcp.example.test", "https://mcp.example.test/mcp",
		"https://user@mcp.example.test", "https://mcp.example.test/?a=1", "https://mcp.example.test/#x", 42} {
		require.ErrorContains(t, validateStartupConfigWithGetter(newMapConfigGetter(withOrigin(origin))), "settings.mcp.public_origin", origin)
	}
}

// TestValidateStartupConfigRefusesUnsafeValidationBounds: startup refuses
// validation budgets that would defeat themselves (maintainer review of PR
// #95, P3 E), such as an oversized verdict cache or a gate allowed more
// one-api calls than one-api's per-IP limit; the defaults still start, and
// the debug-only disabled mode is unmetered and not checked.
func TestValidateStartupConfigRefusesUnsafeValidationBounds(t *testing.T) {
	withValidation := func(validation map[string]any) map[string]any {
		return map[string]any{"settings": map[string]any{"mcp": map[string]any{"auth": map[string]any{"validation": validation}}}}
	}
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(withValidation(map[string]any{"enabled": true}))))

	err := validateStartupConfigWithGetter(newMapConfigGetter(withValidation(map[string]any{"max_entries": 50_000_000})))
	require.ErrorContains(t, err, "settings.mcp.auth.validation.max_entries")
	err = validateStartupConfigWithGetter(newMapConfigGetter(withValidation(map[string]any{"upstream_rate_per_second": 3})))
	require.ErrorContains(t, err, "settings.mcp.auth.validation.oneapi_rate_limit")
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(withValidation(map[string]any{
		"upstream_rate_per_second": 3, "oneapi_rate_limit": 0,
	}))), "an exempt egress may scale the gate")

	disabled := withValidation(map[string]any{"enabled": false, "max_entries": 50_000_000})
	disabled["debug"] = true
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(disabled)))
}
