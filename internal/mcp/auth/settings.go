package auth

import (
	"math"
	"net/netip"
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

// legacyBalanceURLKey is the older balance endpoint override. It named an
// exact endpoint URL, so it is honored verbatim (ValidationSettings.Endpoint)
// when settings.mcp.auth.validation.base_url is unset; startup logs a
// deprecation WARN.
const legacyBalanceURLKey = "settings.oneapi.balance_url"

// Validation bounds that keep a misconfiguration from disabling revocation or flooding one-api.
const (
	maxPositiveTTL = time.Hour
	maxNegativeTTL = 5 * time.Minute
	maxStaleGrace  = 24 * time.Hour
	minTimeout     = 100 * time.Millisecond
	maxTimeout     = 30 * time.Second
)

// Upper bounds CheckValidationBounds enforces. They are sanity ceilings far
// above any deployment's need: a value beyond one is a typo that would
// disable the control it configures, so startup refuses it.
const (
	// maxValidationEntries bounds MaxEntries and MaxSources. Both maps grow
	// with keys and sources an attacker chooses; a million entries is a few
	// hundred MiB at most.
	maxValidationEntries = 1_000_000
	// maxValidationConcurrent bounds simultaneous upstream calls.
	maxValidationConcurrent = 256
	// maxValidationRate bounds every rate, in calls per second.
	maxValidationRate = 1000
	// maxValidationBurst bounds every burst.
	maxValidationBurst = 100_000
	// maxGateways bounds the gateway list, which every budgeted call scans.
	maxGateways = 64
	// minGatewayPrefixIPv4 and minGatewayPrefixIPv6 refuse a gateway range
	// so wide that it would merge unrelated clients into one source.
	minGatewayPrefixIPv4 = 16
	minGatewayPrefixIPv6 = 32
)

// DefaultOneAPIRateLimit is one-api's default GLOBAL_API_RATE_LIMIT: calls
// per OneAPIRateLimitWindow from one client address.
const DefaultOneAPIRateLimit = 480

// ValidationSettings configures how MCP entrypoints validate API keys.
type ValidationSettings struct {
	// Enabled selects the one-api authority. Disabling it is a development-only opt-out.
	Enabled bool
	// BaseURL is the one-api origin; empty uses oneapi.BillingAPI.
	BaseURL string
	// Endpoint is an exact validation URL from the deprecated
	// settings.oneapi.balance_url, used verbatim when BaseURL is unset. A URL
	// that does not end in OneAPITokenBalancePath names no one-api base, so
	// consumes cannot refresh verdicts (consumes_refresh_verdicts=false).
	Endpoint string
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
	// OneAPIRateLimit is one-api's per-IP REST limit for this process's
	// share of its egress address, in calls per OneAPIRateLimitWindow
	// (GLOBAL_API_RATE_LIMIT); 0 means the egress is exempt. The gate may not
	// be configured to start more calls than this, and at run time it starts
	// none once this process's one-api REST calls (consumes included, see
	// oneapi.CallLedger) reach it within the window.
	OneAPIRateLimit int
	// Gateways gives client address ranges their own per-source budget
	// (settings key "gateways": a list of {cidr, rate_per_second, burst}).
	Gateways []GatewaySetting
}

// GatewaySetting is one configured gateway range, as written in settings.
// ParseGateways validates it into a GatewaySource.
type GatewaySetting struct {
	CIDR          string
	RatePerSecond float64
	Burst         int
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
		OneAPIRateLimit:           DefaultOneAPIRateLimit,
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
		// Honor the pre-existing balance-proxy override for self-hosted
		// one-api exactly as it was used: as the endpoint URL itself.
		if raw, ok := get(legacyBalanceURLKey).(string); ok {
			settings.Endpoint = strings.TrimSpace(raw)
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
	settings.OneAPIRateLimit = intValue(value("oneapi_rate_limit"), settings.OneAPIRateLimit)
	settings.Gateways = gatewaySettings(value("gateways"))
	return settings
}

// gatewaySettings reads the gateway list as config loaders deliver it: a
// list of maps. Entries of another shape become an empty setting, which
// ParseGateways refuses, so a typo fails startup instead of vanishing.
func gatewaySettings(raw any) []GatewaySetting {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	gateways := make([]GatewaySetting, 0, len(list))
	for _, item := range list {
		var fields map[string]any
		switch typed := item.(type) {
		case map[string]any:
			fields = typed
		case map[any]any:
			fields = make(map[string]any, len(typed))
			for key, value := range typed {
				if name, ok := key.(string); ok {
					fields[name] = value
				}
			}
		}
		cidr, _ := fields["cidr"].(string)
		gateways = append(gateways, GatewaySetting{
			CIDR:          strings.TrimSpace(cidr),
			RatePerSecond: floatValue(fields["rate_per_second"], 0),
			Burst:         intValue(fields["burst"], 0),
		})
	}
	return gateways
}

// ParseGateways validates the configured gateways against the global
// budget: each range is a valid prefix (a bare address is one host) no wider
// than /16 for IPv4 or /32 for IPv6, ranges do not overlap, and each budget
// is positive and within upstream_rate_per_second and upstream_burst, so a
// gateway can never spend more than the global budget all sources share.
func ParseGateways(s ValidationSettings) ([]GatewaySource, error) {
	if len(s.Gateways) > maxGateways {
		return nil, errors.Errorf("%sgateways lists %d ranges; at most %d are allowed", validationConfigPrefix, len(s.Gateways), maxGateways)
	}
	var errs []error
	parsed := make([]GatewaySource, 0, len(s.Gateways))
	for index, gateway := range s.Gateways {
		refuse := func(format string, args ...any) {
			errs = append(errs, errors.Errorf("%sgateways[%d] "+format, append([]any{validationConfigPrefix, index}, args...)...))
		}
		prefix, err := parseGatewayPrefix(gateway.CIDR)
		if err != nil {
			refuse("cidr=%q: %v", gateway.CIDR, err)
			continue
		}
		switch {
		case math.IsNaN(gateway.RatePerSecond) || gateway.RatePerSecond <= 0 || gateway.RatePerSecond > s.UpstreamRatePerSecond:
			refuse("rate_per_second=%v must be positive and at most upstream_rate_per_second=%v", gateway.RatePerSecond, s.UpstreamRatePerSecond)
			continue
		case gateway.Burst < 1 || gateway.Burst > s.UpstreamBurst:
			refuse("burst=%d must be between 1 and upstream_burst=%d", gateway.Burst, s.UpstreamBurst)
			continue
		}
		for _, previous := range parsed {
			if previous.Prefix.Overlaps(prefix) {
				refuse("cidr=%s overlaps %s", prefix, previous.Prefix)
			}
		}
		parsed = append(parsed, GatewaySource{Prefix: prefix, RatePerSecond: gateway.RatePerSecond, Burst: gateway.Burst})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return parsed, nil
}

// parseGatewayPrefix parses a CIDR or a bare address (one host) and refuses
// ranges wider than the minimum prefix lengths.
func parseGatewayPrefix(raw string) (netip.Prefix, error) {
	if raw == "" {
		return netip.Prefix{}, errors.New("a cidr is required")
	}
	if !strings.Contains(raw, "/") {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return netip.Prefix{}, errors.Wrap(err, "parse address")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, errors.Wrap(err, "parse prefix")
	}
	minBits := minGatewayPrefixIPv6
	if prefix.Addr().Unmap().Is4() {
		minBits = minGatewayPrefixIPv4
		if prefix.Addr().Is4In6() {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), max(prefix.Bits()-96, 0))
		}
	}
	if prefix.Bits() < minBits {
		return netip.Prefix{}, errors.Errorf("prefix /%d is wider than /%d", prefix.Bits(), minBits)
	}
	return prefix.Masked(), nil
}

// CheckValidationBounds refuses settings that would defeat the controls they
// configure (maintainer review of PR #95): an oversized verdict cache or
// source map, which grow with attacker-chosen keys and sources; unbounded
// concurrency, rates or bursts; a per-source budget above the global one,
// which then no longer keeps one source from spending it all; and a gate that
// may start more one-api calls per window than one-api's per-IP limit, which
// would leave nothing for the paid-tool consumes sharing it. Disabled
// validation (a --debug opt-out) is unmetered and not checked.
func CheckValidationBounds(s ValidationSettings) error {
	if !s.Enabled {
		return nil
	}
	var errs []error
	refuse := func(format string, args ...any) {
		errs = append(errs, errors.Errorf(validationConfigPrefix+format, args...))
	}
	if _, err := s.newValidator(); err != nil {
		errs = append(errs, err)
	}
	if _, err := ParseGateways(s); err != nil {
		errs = append(errs, err)
	}
	for _, bound := range []struct {
		key   string
		value int
		limit int
	}{
		{"max_entries", s.MaxEntries, maxValidationEntries},
		{"max_sources", s.MaxSources, maxValidationEntries},
		{"max_concurrent", s.MaxConcurrent, maxValidationConcurrent},
		{"upstream_burst", s.UpstreamBurst, maxValidationBurst},
		{"per_source_burst", s.PerSourceBurst, maxValidationBurst},
		{"revalidation_burst", s.RevalidationBurst, maxValidationBurst},
	} {
		if bound.value > bound.limit {
			refuse("%s=%d exceeds the maximum %d", bound.key, bound.value, bound.limit)
		}
	}
	for _, rate := range []struct {
		key   string
		value float64
	}{
		{"upstream_rate_per_second", s.UpstreamRatePerSecond},
		{"per_source_rate_per_second", s.PerSourceRatePerSecond},
		{"revalidation_rate_per_second", s.RevalidationRatePerSecond},
	} {
		if math.IsNaN(rate.value) || math.IsInf(rate.value, 0) || rate.value > maxValidationRate {
			refuse("%s=%v must be a finite rate of at most %d per second", rate.key, rate.value, maxValidationRate)
		}
	}
	if s.PerSourceRatePerSecond > s.UpstreamRatePerSecond {
		refuse("per_source_rate_per_second=%v exceeds upstream_rate_per_second=%v; one source could then spend the whole global budget",
			s.PerSourceRatePerSecond, s.UpstreamRatePerSecond)
	}
	if s.PerSourceBurst > s.UpstreamBurst {
		refuse("per_source_burst=%d exceeds upstream_burst=%d; one source could then spend the whole global burst",
			s.PerSourceBurst, s.UpstreamBurst)
	}
	switch calls := s.Capacity().MaxCallsPerWindow; {
	case s.OneAPIRateLimit < 0:
		refuse("oneapi_rate_limit=%d must be one-api's per-IP limit per %s, or 0 when this server's egress is exempt",
			s.OneAPIRateLimit, OneAPIRateLimitWindow)
	case s.OneAPIRateLimit > 0 && calls > s.OneAPIRateLimit:
		refuse("oneapi_rate_limit=%d is below the %d calls per %s the validation gate may start "+
			"(upstream rate and burst plus the revalidation reserve), which would leave no room for paid-tool consumes; "+
			"lower those settings, or raise oneapi_rate_limit to match one-api's GLOBAL_API_RATE_LIMIT (0 when exempt)",
			s.OneAPIRateLimit, calls, OneAPIRateLimitWindow)
	}
	return errors.Join(errs...)
}

// OneAPIRateLimitWindow is the window of one-api's per-IP REST rate limit
// (GLOBAL_API_RATE_LIMIT, by default 480 calls per 3 minutes).
const OneAPIRateLimitWindow = oneapi.RateLimitWindow

// ValidationCapacity is what the upstream budgets of one process sustain.
// Keys whose tool calls keep being accepted by one-api's consume need no
// revalidation (Authenticator.BeginAuthorityCall) and do not count here.
type ValidationCapacity struct {
	// ActiveKeys is how many keys can each revalidate once per PositiveTTL
	// through the reserve and the global budget together.
	ActiveKeys int
	// ActiveKeysUnderFlood is the same through the reserve alone, while a
	// flood from many sources spends the global budget.
	ActiveKeysUnderFlood int
	// MaxCallsPerWindow bounds the new-key, balance and revalidation calls
	// the gate can start per OneAPIRateLimitWindow; consumes come on top.
	MaxCallsPerWindow int
}

// Capacity derives the capacity of these settings for one process. Budgets
// are process-local: replicas sharing one egress address add up at one-api.
func (s ValidationSettings) Capacity() ValidationCapacity {
	ttl, window := s.PositiveTTL.Seconds(), OneAPIRateLimitWindow.Seconds()
	reserveRate, reserveBurst := 0.0, 0
	if s.RevalidationRatePerSecond > 0 {
		reserveRate, reserveBurst = s.RevalidationRatePerSecond, s.RevalidationBurst
	}
	return ValidationCapacity{
		ActiveKeys:           int(math.Round((reserveRate + s.UpstreamRatePerSecond) * ttl)),
		ActiveKeysUnderFlood: int(math.Round(reserveRate * ttl)),
		MaxCallsPerWindow: int(math.Round(s.UpstreamRatePerSecond*window)) + s.UpstreamBurst +
			int(math.Round(reserveRate*window)) + reserveBurst,
	}
}

// CheckValidationMode refuses the accept-all opt-out outside debug mode: in
// the server's prod mode (no --debug) a disabled validation would let any
// non-empty bearer become a tenant, which reopens issue #86. Development
// hosts without one-api run with --debug.
func CheckValidationMode(settings ValidationSettings, debug bool) error {
	if settings.Enabled || debug {
		return nil
	}
	return errors.Errorf("%senabled=false is a development-only opt-out and is refused in prod mode; "+
		"run with --debug or enable one-api validation", validationConfigPrefix)
}

// newValidator builds the one-api validator these settings select: the
// exact legacy Endpoint, else BaseURL (or oneapi.BillingAPI) plus
// OneAPITokenBalancePath.
func (s ValidationSettings) newValidator() (*OneAPITokenValidator, error) {
	if s.BaseURL == "" && s.Endpoint != "" {
		validator, err := NewOneAPITokenValidatorAt(s.Endpoint, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "%s", legacyBalanceURLKey)
		}
		return validator, nil
	}
	baseURL := s.BaseURL
	if baseURL == "" {
		baseURL = oneapi.BillingAPI
	}
	validator, err := NewOneAPITokenValidator(baseURL, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "%sbase_url", validationConfigPrefix)
	}
	return validator, nil
}

// NewAuthenticatorFromSettings builds the production Authenticator: the one-api
// validator when enabled, otherwise an explicit accept-all opt-out logged at WARN.
// The enabled gate admits against the process-wide oneapi.DefaultCallLedger,
// which every consume also records into.
func NewAuthenticatorFromSettings(settings ValidationSettings, logger logSDK.Logger) (*Authenticator, error) {
	if !settings.Enabled {
		if logger != nil {
			logger.Warn("MCP API key validation is DISABLED (settings.mcp.auth.validation.enabled=false); " +
				"any non-empty bearer becomes a tenant. Never use this outside local development.")
		}
		authMetrics.oneapiRateLimit.Set(0)
		authenticator, err := NewAuthenticator(InsecureAcceptAllValidator{}, UnmeteredOptions("disabled"))
		return authenticator, errors.WithStack(err)
	}

	gateways, err := ParseGateways(settings)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	validator, err := settings.newValidator()
	if err != nil {
		return nil, errors.Wrap(err, "build one-api api key validator")
	}
	options := AuthenticatorOptions{
		Name:                      "oneapi",
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
		Gateways:                  gateways,
		Ledger:                    oneapi.DefaultCallLedger(),
		OneAPIRateLimit:           settings.OneAPIRateLimit,
	}
	authenticator, err := NewAuthenticator(validator, options)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	authMetrics.oneapiRateLimit.Set(float64(settings.OneAPIRateLimit))
	if logger != nil {
		if settings.BaseURL == "" && settings.Endpoint != "" {
			logger.Warn("settings.oneapi.balance_url is deprecated; set settings.mcp.auth.validation.base_url to the one-api origin instead",
				zap.String("endpoint", validator.Endpoint()),
				zap.Bool("names_oneapi_base", validator.AuthorityBaseURL() != ""))
		}
		capacity := settings.Capacity()
		logger.Info("MCP API key validation enabled",
			zap.String("validator", options.Name),
			zap.String("endpoint", validator.Endpoint()),
			zap.Duration("positive_ttl", settings.PositiveTTL),
			zap.Duration("negative_ttl", settings.NegativeTTL),
			zap.Duration("stale_grace", settings.StaleGrace),
			// Per-process capacity; compare it with the deployment's peak of
			// simultaneously active keys (docs/manual/mcp.md, Capacity and scaling).
			zap.Int("active_key_capacity", capacity.ActiveKeys),
			zap.Int("active_key_capacity_under_flood", capacity.ActiveKeysUnderFlood),
			zap.Int("max_oneapi_calls_per_3m", capacity.MaxCallsPerWindow),
			zap.Int("oneapi_rate_limit", settings.OneAPIRateLimit),
			zap.Int("gateways", len(gateways)),
			zap.Bool("consumes_refresh_verdicts", authenticator.ObservesAuthority(oneapi.BillingAPI)),
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
