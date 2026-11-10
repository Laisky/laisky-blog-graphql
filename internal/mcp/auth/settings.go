package auth

import (
	"strconv"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// validationConfigPrefix is the settings namespace of API key validation.
const validationConfigPrefix = "settings.mcp.auth.validation."

// legacyBalanceURLKey is the older balance endpoint override, honored as the
// one-api origin when settings.mcp.auth.validation.base_url is unset.
const legacyBalanceURLKey = "settings.oneapi.balance_url"

// Validation bounds that keep a misconfiguration from disabling revocation or flooding one-api.
const (
	maxPositiveTTL = time.Hour
	maxNegativeTTL = 5 * time.Minute
	maxStaleGrace  = 24 * time.Hour
	minTimeout     = 100 * time.Millisecond
	maxTimeout     = 30 * time.Second
)

// ValidationSettings configures how MCP entrypoints validate API keys.
type ValidationSettings struct {
	// Enabled selects the one-api authority. Disabling it is a development-only opt-out.
	Enabled bool
	// BaseURL is the one-api origin; empty uses oneapi.BillingAPI.
	BaseURL string
	// Timeout bounds one upstream validation.
	Timeout time.Duration
	// PositiveTTL is how long an accepted key is trusted (revocation lag).
	PositiveTTL time.Duration
	// NegativeTTL is how long a rejected key is refused without asking again.
	NegativeTTL time.Duration
	// StaleGrace keeps a recently accepted key usable while one-api is unavailable.
	StaleGrace time.Duration
	// MaxEntries bounds each of the positive and negative caches.
	MaxEntries int
	// MaxConcurrent bounds simultaneous upstream validations.
	MaxConcurrent int
	// UpstreamRatePerSecond and UpstreamBurst bound validations of uncached keys,
	// leaving one-api's per-IP API budget for billing calls.
	UpstreamRatePerSecond float64
	UpstreamBurst         int
	// PerSourceRatePerSecond and PerSourceBurst bound the upstream validations
	// one client source (an IPv4 address or IPv6 /64) may start.
	PerSourceRatePerSecond float64
	PerSourceBurst         int
	// MaxSources bounds the tracked client sources (least recently used evicted).
	MaxSources int
	// RevalidationRatePerSecond and RevalidationBurst reserve upstream calls
	// for keys one-api accepted before; 0 disables the reserve.
	RevalidationRatePerSecond float64
	RevalidationBurst         int
}

// DefaultValidationSettings returns the production defaults: one-api validation enabled.
func DefaultValidationSettings() ValidationSettings {
	return ValidationSettings{
		Enabled:                true,
		Timeout:                5 * time.Second,
		PositiveTTL:            5 * time.Minute,
		NegativeTTL:            30 * time.Second,
		StaleGrace:             30 * time.Minute,
		MaxEntries:             10000,
		MaxConcurrent:          8,
		UpstreamRatePerSecond:  1,
		UpstreamBurst:          30,
		PerSourceRatePerSecond: 0.1,
		PerSourceBurst:         5,
		MaxSources:             10000,
		// 0.2/s supports about 60 active keys per 5-minute TTL; with the
		// global budget this stays at most 266 calls per 3 minutes.
		RevalidationRatePerSecond: 0.2,
		RevalidationBurst:         20,
	}
}

// LoadValidationSettingsFromConfig reads settings.mcp.auth.validation.* from the shared config.
func LoadValidationSettingsFromConfig() ValidationSettings {
	return LoadValidationSettings(func(key string) any { return gconfig.S.Get(key) })
}

// LoadValidationSettings reads settings.mcp.auth.validation.* through get and
// clamps every value into a safe range; absent keys keep the defaults.
func LoadValidationSettings(get func(string) any) ValidationSettings {
	settings := DefaultValidationSettings()
	if get == nil {
		return settings
	}
	value := func(name string) any { return get(validationConfigPrefix + name) }

	settings.Enabled = boolValue(value("enabled"), settings.Enabled)
	if raw, ok := value("base_url").(string); ok {
		settings.BaseURL = strings.TrimSpace(raw)
	}
	if settings.BaseURL == "" {
		// Honor the pre-existing balance-proxy override for self-hosted one-api.
		if raw, ok := get(legacyBalanceURLKey).(string); ok {
			settings.BaseURL = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(raw), "/"), OneAPITokenBalancePath)
		}
	}
	settings.Timeout = clampDuration(durationValue(value("timeout"), settings.Timeout), minTimeout, maxTimeout)
	settings.PositiveTTL = clampDuration(durationValue(value("positive_ttl"), settings.PositiveTTL), time.Second, maxPositiveTTL)
	settings.NegativeTTL = clampDuration(durationValue(value("negative_ttl"), settings.NegativeTTL), time.Second, maxNegativeTTL)
	settings.StaleGrace = clampDuration(durationValue(value("stale_grace"), settings.StaleGrace), 0, maxStaleGrace)
	settings.MaxEntries = max(1, intValue(value("max_entries"), settings.MaxEntries))
	settings.MaxConcurrent = max(1, intValue(value("max_concurrent"), settings.MaxConcurrent))
	settings.UpstreamRatePerSecond = floatValue(value("upstream_rate_per_second"), settings.UpstreamRatePerSecond)
	if settings.UpstreamRatePerSecond <= 0 {
		settings.UpstreamRatePerSecond = DefaultValidationSettings().UpstreamRatePerSecond
	}
	settings.UpstreamBurst = max(1, intValue(value("upstream_burst"), settings.UpstreamBurst))
	settings.PerSourceRatePerSecond = floatValue(value("per_source_rate_per_second"), settings.PerSourceRatePerSecond)
	if settings.PerSourceRatePerSecond <= 0 {
		settings.PerSourceRatePerSecond = DefaultValidationSettings().PerSourceRatePerSecond
	}
	settings.PerSourceBurst = max(1, intValue(value("per_source_burst"), settings.PerSourceBurst))
	settings.MaxSources = max(1, intValue(value("max_sources"), settings.MaxSources))
	// An explicit 0 disables the reserve; unset keeps the default.
	settings.RevalidationRatePerSecond = max(0, floatValue(value("revalidation_rate_per_second"), settings.RevalidationRatePerSecond))
	settings.RevalidationBurst = max(1, intValue(value("revalidation_burst"), settings.RevalidationBurst))
	return settings
}

// NewAuthenticatorFromSettings builds the production Authenticator: the one-api
// validator when enabled, otherwise an explicit accept-all opt-out logged at WARN.
func NewAuthenticatorFromSettings(settings ValidationSettings, logger logSDK.Logger) (*Authenticator, error) {
	options := AuthenticatorOptions{
		PositiveTTL:               settings.PositiveTTL,
		NegativeTTL:               settings.NegativeTTL,
		Timeout:                   settings.Timeout,
		MaxEntries:                settings.MaxEntries,
		MaxConcurrent:             settings.MaxConcurrent,
		UpstreamRatePerSecond:     settings.UpstreamRatePerSecond,
		UpstreamBurst:             settings.UpstreamBurst,
		PerSourceRatePerSecond:    settings.PerSourceRatePerSecond,
		PerSourceBurst:            settings.PerSourceBurst,
		MaxSources:                settings.MaxSources,
		StaleGrace:                disabledIfZero(settings.StaleGrace),
		RevalidationRatePerSecond: disabledIfZeroRate(settings.RevalidationRatePerSecond),
		RevalidationBurst:         settings.RevalidationBurst,
	}
	if !settings.Enabled {
		if logger != nil {
			logger.Warn("MCP API key validation is DISABLED (settings.mcp.auth.validation.enabled=false); " +
				"any non-empty bearer becomes a tenant. Never use this outside local development.")
		}
		authenticator, err := NewAuthenticator(InsecureAcceptAllValidator{}, UnmeteredOptions("disabled"))
		return authenticator, errors.WithStack(err)
	}

	baseURL := settings.BaseURL
	if baseURL == "" {
		baseURL = oneapi.BillingAPI
	}
	validator, err := NewOneAPITokenValidator(baseURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "build one-api api key validator")
	}
	options.Name = "oneapi"
	authenticator, err := NewAuthenticator(validator, options)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if logger != nil {
		logger.Info("MCP API key validation enabled",
			zap.String("validator", options.Name),
			zap.String("endpoint", validator.Endpoint()),
			zap.Duration("positive_ttl", settings.PositiveTTL),
			zap.Duration("negative_ttl", settings.NegativeTTL),
			zap.Duration("stale_grace", settings.StaleGrace),
		)
	}
	return authenticator, nil
}

// disabledIfZero maps an explicit zero setting to the options' "disabled" marker.
func disabledIfZero(value time.Duration) time.Duration {
	if value == 0 {
		return -1
	}
	return value
}

// disabledIfZeroRate maps an explicit zero rate to the options' "disabled" marker.
func disabledIfZeroRate(value float64) float64 {
	if value == 0 {
		return -1
	}
	return value
}

// clampDuration keeps a duration within [low, high].
func clampDuration(value, low, high time.Duration) time.Duration {
	return min(max(value, low), high)
}

// boolValue coerces common config representations of a boolean.
func boolValue(raw any, fallback bool) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	}
	return fallback
}

// durationValue accepts Go duration strings, time.Duration, or integer seconds.
func durationValue(raw any, fallback time.Duration) time.Duration {
	switch value := raw.(type) {
	case time.Duration:
		return value
	case string:
		if parsed, err := time.ParseDuration(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	case int:
		return time.Duration(value) * time.Second
	case int64:
		return time.Duration(value) * time.Second
	case float64:
		return time.Duration(value * float64(time.Second))
	}
	return fallback
}

// intValue coerces numeric config values.
func intValue(raw any, fallback int) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

// floatValue coerces numeric config values.
func floatValue(raw any, fallback float64) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return parsed
		}
	}
	return fallback
}
