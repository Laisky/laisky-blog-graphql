package auth

import (
	"math"
	"path/filepath"
	"runtime"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// loadWith loads validation settings with values overriding the defaults.
func loadWith(values map[string]any) ValidationSettings {
	return LoadValidationSettings(func(key string) any { return values[key] })
}

// TestValidationSettingsRefuseUnsafeBounds (maintainer review of PR #95, P3
// E): the loader clamped TTLs but accepted any cache size, burst,
// concurrency or rate, so one typo could disable the controls it configures —
// an unbounded verdict cache or source map grows with attacker-chosen keys,
// a per-source budget above the global one stops protecting other callers,
// and a gate allowed more calls than one-api's per-IP limit starves the
// paid-tool consumes that share it. CheckValidationBounds refuses those at
// startup; the defaults and every documented example stay accepted.
func TestValidationSettingsRefuseUnsafeBounds(t *testing.T) {
	require.NoError(t, CheckValidationBounds(DefaultValidationSettings()))
	require.NoError(t, CheckValidationBounds(loadWith(nil)))

	prefix := validationConfigPrefix
	for name, tc := range map[string]struct {
		values map[string]any
		key    string
	}{
		"cache size":                    {map[string]any{prefix + "max_entries": 50_000_000}, "max_entries"},
		"source map":                    {map[string]any{prefix + "max_sources": 50_000_000}, "max_sources"},
		"concurrency":                   {map[string]any{prefix + "max_concurrent": 10_000}, "max_concurrent"},
		"non-finite upstream rate":      {map[string]any{prefix + "upstream_rate_per_second": math.Inf(1)}, "upstream_rate_per_second"},
		"NaN per-source rate":           {map[string]any{prefix + "per_source_rate_per_second": "NaN"}, "per_source_rate_per_second"},
		"NaN revalidation rate":         {map[string]any{prefix + "revalidation_rate_per_second": "NaN"}, "revalidation_rate_per_second"},
		"per-source rate above global":  {map[string]any{prefix + "per_source_rate_per_second": 2}, "per_source_rate_per_second"},
		"per-source burst above global": {map[string]any{prefix + "per_source_burst": 31}, "per_source_burst"},
		"gate above one-api's limit":    {map[string]any{prefix + "upstream_rate_per_second": 3}, "oneapi_rate_limit"},
		"burst above one-api's limit":   {map[string]any{prefix + "upstream_burst": 500, prefix + "per_source_burst": 5}, "oneapi_rate_limit"},
		"revalidation above the limit":  {map[string]any{prefix + "revalidation_rate_per_second": 2}, "oneapi_rate_limit"},
		"exempt egress, absurd burst":   {map[string]any{prefix + "oneapi_rate_limit": 0, prefix + "upstream_burst": 1_000_000}, "upstream_burst"},
		"exempt egress, absurd rate":    {map[string]any{prefix + "oneapi_rate_limit": 0, prefix + "upstream_rate_per_second": 1e6}, "upstream_rate_per_second"},
		"negative one-api limit":        {map[string]any{prefix + "oneapi_rate_limit": -1}, "oneapi_rate_limit"},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckValidationBounds(loadWith(tc.values))
			require.Error(t, err)
			require.ErrorContains(t, err, prefix+tc.key)
		})
	}

	// An egress exempt from one-api's per-IP limit may scale the gate.
	scaled := loadWith(map[string]any{
		prefix + "oneapi_rate_limit":            0,
		prefix + "upstream_rate_per_second":     20,
		prefix + "upstream_burst":               600,
		prefix + "revalidation_rate_per_second": 5,
	})
	require.NoError(t, CheckValidationBounds(scaled))
	// A raised one-api limit admits a proportionally larger gate.
	raised := loadWith(map[string]any{prefix + "oneapi_rate_limit": 2400, prefix + "upstream_rate_per_second": 10})
	require.NoError(t, CheckValidationBounds(raised))
	require.Equal(t, 2400, raised.OneAPIRateLimit)
	require.Equal(t, DefaultOneAPIRateLimit, loadWith(nil).OneAPIRateLimit)
}

// TestExampleSettingsPassValidationBounds keeps docs/example/config/settings.yml
// inside the bounds, so the documented configuration starts. It is read with
// the production config loader.
func TestExampleSettingsPassValidationBounds(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	cfg := gconfig.New()
	require.NoError(t, cfg.LoadFromFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "example", "config", "settings.yml")))
	settings := LoadValidationSettings(cfg.Get)
	require.NoError(t, CheckValidationBounds(settings))
	require.Equal(t, 10000, settings.MaxEntries, "the example is actually read")
}
