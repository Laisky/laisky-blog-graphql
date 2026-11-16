package auth

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// TestGateYieldsToConsumesInOneAPIWindow covers finding P2 (third maintainer
// review of PR #95): consumes share one-api's per-IP REST limit with the
// gate, so the gate must count them. Once this process's calls within the
// window reach oneapi_rate_limit, the gate starts no validation, balance or
// revalidation call, and that local refusal never grants stale grace.
func TestGateYieldsToConsumesInOneAPIWindow(t *testing.T) {
	ledger := oneapi.NewCallLedger(oneapi.RateLimitWindow)
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil, "sk-new": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{
		Ledger: ledger, OneAPIRateLimit: 10, PositiveTTL: time.Minute, StaleGrace: time.Hour,
	})
	ctx := clientip.WithIP(context.Background(), "192.0.2.10")

	_, err := authenticator.Authenticate(ctx, "Bearer sk-known")
	require.NoError(t, err)
	require.Equal(t, 1, ledger.Count(clock.Now()), "the gate records its own calls")

	for range 9 {
		ledger.Record(clock.Now(), oneapi.RouteTokenConsume)
	}
	before := testutil.ToFloat64(authMetrics.gateRefusals.WithLabelValues(refusalWindow))
	_, err = authenticator.Authenticate(ctx, "Bearer sk-new")
	require.ErrorIs(t, err, ErrUpstreamThrottled, "the window is full: one more call would draw a 429 for the consumes")
	require.Equal(t, int64(1), authority.calls.Load())
	require.InDelta(t, before+1, testutil.ToFloat64(authMetrics.gateRefusals.WithLabelValues(refusalWindow)), 0)

	clock.Advance(2 * time.Minute) // sk-known needs a revalidation, inside its stale grace
	for range 10 {
		ledger.Record(clock.Now(), oneapi.RouteTokenConsume)
	}
	authCtx, err := authenticator.Authenticate(ctx, "Bearer sk-known")
	require.Nil(t, authCtx, "a local refusal never extends a key's life")
	require.ErrorIs(t, err, ErrUpstreamThrottled)

	clock.Advance(oneapi.RateLimitWindow + time.Second)
	_, err = authenticator.Authenticate(ctx, "Bearer sk-new")
	require.NoError(t, err, "calls resume once the window drains")
}

// TestProductionGateSharesTheConsumeLedger: the production authenticator
// admits against the same ledger every consume records into.
func TestProductionGateSharesTheConsumeLedger(t *testing.T) {
	authenticator, err := NewAuthenticatorFromSettings(DefaultValidationSettings(), nil)
	require.NoError(t, err)
	require.Same(t, oneapi.DefaultCallLedger(), authenticator.gate.ledger)
	require.Equal(t, DefaultOneAPIRateLimit, authenticator.gate.windowLimit)
}

// onboardKeys sends one request per new key from one client address on a
// fake clock, one key per interval, and returns how many were accepted.
func onboardKeys(t *testing.T, opts AuthenticatorOptions, address string, keys int, interval time.Duration) int {
	t.Helper()
	authenticator, clock := newScriptedAuthenticatorFor(t, capacityAuthority{}, opts)
	accepted := 0
	for key := range keys {
		ctx := clientip.WithIP(context.Background(), address)
		if _, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-active-%d", key)); err == nil {
			accepted++
		} else {
			require.True(t, IsUnavailable(err), "an issued key is never rejected: %v", err)
		}
		clock.Advance(interval)
	}
	return accepted
}

// TestSharedSourceOnboardingCapacity covers finding P2 (per-source limits
// restrict legitimate multi-tenant clients): many independent keys behind
// one NAT or gateway share one source. With the defaults one address can
// onboard per_source_burst keys and then one every 10 seconds; a configured
// gateway range gets its own budget, still within the global one.
func TestSharedSourceOnboardingCapacity(t *testing.T) {
	const keys, interval = 100, time.Second // 100 new users in 100 seconds

	t.Run("defaults limit one address to its source budget", func(t *testing.T) {
		// Calls at t = 0..99 s: the burst of 5, plus 0.1/s refilled over 99 s.
		require.Equal(t, 5+9, onboardKeys(t, AuthenticatorOptions{}, "203.0.113.7", keys, interval))
	})
	t.Run("a gateway range gets its configured budget", func(t *testing.T) {
		opts := AuthenticatorOptions{Gateways: []GatewaySource{{
			Prefix: netip.MustParsePrefix("203.0.113.0/28"), RatePerSecond: 1, Burst: 30,
		}}}
		require.Equal(t, keys, onboardKeys(t, opts, "203.0.113.7", keys, interval))
	})
	t.Run("the gateway budget never exceeds the global one", func(t *testing.T) {
		opts := AuthenticatorOptions{UpstreamRatePerSecond: 0.5, UpstreamBurst: 10, Gateways: []GatewaySource{{
			Prefix: netip.MustParsePrefix("203.0.113.7/32"), RatePerSecond: 0.5, Burst: 10,
		}}}
		require.Equal(t, 10+49, onboardKeys(t, opts, "203.0.113.7", keys, interval), "the burst of 10 plus 0.5/s over 99 s")
	})
	t.Run("addresses outside the range keep the default budget", func(t *testing.T) {
		opts := AuthenticatorOptions{Gateways: []GatewaySource{{
			Prefix: netip.MustParsePrefix("203.0.113.0/28"), RatePerSecond: 1, Burst: 30,
		}}}
		require.Equal(t, 5+9, onboardKeys(t, opts, "203.0.113.200", keys, interval))
	})
}

// TestGatewayAddressesShareOneBudget: every address of a gateway range is
// one source, so rotating addresses inside the range gains nothing.
func TestGatewayAddressesShareOneBudget(t *testing.T) {
	authenticator, _ := newScriptedAuthenticatorFor(t, capacityAuthority{}, AuthenticatorOptions{Gateways: []GatewaySource{{
		Prefix: netip.MustParsePrefix("2001:db8::/48"), RatePerSecond: 0.01, Burst: 3,
	}}})
	accepted := 0
	for key := range 10 {
		ctx := clientip.WithIP(context.Background(), fmt.Sprintf("2001:db8:0:%x::1", key))
		if _, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-active-%d", key)); err == nil {
			accepted++
		}
	}
	require.Equal(t, 3, accepted, "ten /64s inside one gateway /48 share its burst")
}

// TestNamedBudgetSourceIsIndependentOfAddresses: callers without a client
// address (Telegram) are charged to their named source, not to "unknown".
func TestNamedBudgetSourceIsIndependentOfAddresses(t *testing.T) {
	authenticator, _ := newScriptedAuthenticatorFor(t, capacityAuthority{}, AuthenticatorOptions{})
	spend := func(ctx context.Context, prefix string) int {
		accepted := 0
		for key := range 10 {
			if _, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-active-%s-%d", prefix, key)); err == nil {
				accepted++
			}
		}
		return accepted
	}
	require.Equal(t, 5, spend(WithBudgetSource(context.Background(), "telegram:1"), "a"))
	require.Equal(t, 5, spend(WithBudgetSource(context.Background(), "telegram:2"), "b"), "another chat has its own budget")
	require.Equal(t, 5, spend(context.Background(), "c"), "callers without any source share the unknown budget")
	require.Equal(t, context.Background(), WithBudgetSource(context.Background(), " "), "an empty name changes nothing")
}

// TestParseGatewaysRefusesMisconfiguration keeps a gateway from defeating
// per-source fairness or the global budget.
func TestParseGatewaysRefusesMisconfiguration(t *testing.T) {
	base := DefaultValidationSettings()
	parse := func(gateways ...GatewaySetting) error {
		settings := base
		settings.Gateways = gateways
		_, err := ParseGateways(settings)
		return err
	}
	require.NoError(t, parse(GatewaySetting{CIDR: "203.0.113.10", RatePerSecond: 0.5, Burst: 10}))
	require.NoError(t, parse(GatewaySetting{CIDR: "2001:db8::/48", RatePerSecond: 1, Burst: 30}))
	for name, gateway := range map[string]GatewaySetting{
		"missing cidr":       {RatePerSecond: 0.5, Burst: 1},
		"bad cidr":           {CIDR: "203.0.113.0/33", RatePerSecond: 0.5, Burst: 1},
		"too wide ipv4":      {CIDR: "10.0.0.0/8", RatePerSecond: 0.5, Burst: 1},
		"too wide ipv6":      {CIDR: "2001:db8::/16", RatePerSecond: 0.5, Burst: 1},
		"too wide mapped":    {CIDR: "::ffff:10.0.0.0/104", RatePerSecond: 0.5, Burst: 1},
		"zero rate":          {CIDR: "203.0.113.10", Burst: 1},
		"rate above global":  {CIDR: "203.0.113.10", RatePerSecond: 2, Burst: 1},
		"zero burst":         {CIDR: "203.0.113.10", RatePerSecond: 0.5},
		"burst above global": {CIDR: "203.0.113.10", RatePerSecond: 0.5, Burst: 31},
	} {
		require.Error(t, parse(gateway), name)
	}
	require.Error(t, parse(
		GatewaySetting{CIDR: "203.0.113.0/24", RatePerSecond: 0.5, Burst: 1},
		GatewaySetting{CIDR: "203.0.113.7", RatePerSecond: 0.5, Burst: 1},
	), "overlapping ranges are ambiguous")
	many := make([]GatewaySetting, maxGateways+1)
	for i := range many {
		many[i] = GatewaySetting{CIDR: fmt.Sprintf("198.51.100.%d", i), RatePerSecond: 0.1, Burst: 1}
	}
	require.Error(t, parse(many...))

	invalid := base
	invalid.Gateways = []GatewaySetting{{CIDR: "nonsense", RatePerSecond: 0.5, Burst: 1}}
	require.Error(t, CheckValidationBounds(invalid), "startup validation reports gateway errors")
	_, err := NewAuthenticatorFromSettings(invalid, nil)
	require.Error(t, err)
}

// TestLoadValidationSettingsReadsGatewayLists reads the list the way the
// YAML config loader delivers it.
func TestLoadValidationSettingsReadsGatewayLists(t *testing.T) {
	settings := LoadValidationSettings(func(key string) any {
		return map[string]any{validationConfigPrefix + "gateways": []any{
			map[string]any{"cidr": "203.0.113.10/32", "rate_per_second": 0.5, "burst": 20},
			map[any]any{"cidr": " 2001:db8::/48 ", "rate_per_second": "1", "burst": "30"},
			"not a map",
		}}[key]
	})
	require.Equal(t, []GatewaySetting{
		{CIDR: "203.0.113.10/32", RatePerSecond: 0.5, Burst: 20},
		{CIDR: "2001:db8::/48", RatePerSecond: 1, Burst: 30},
		{},
	}, settings.Gateways)
	require.Error(t, CheckValidationBounds(settings), "the malformed entry fails startup instead of vanishing")
}

// TestCheckValidationBoundsRefusesUnusableAuthorityURLs: a bad base_url or
// legacy endpoint fails startup validation, not the first request.
func TestCheckValidationBoundsRefusesUnusableAuthorityURLs(t *testing.T) {
	for _, settings := range []ValidationSettings{
		func() ValidationSettings {
			s := DefaultValidationSettings()
			s.BaseURL = "ftp://oneapi.example.com"
			return s
		}(),
		func() ValidationSettings {
			s := DefaultValidationSettings()
			s.BaseURL = "https://user:pw@oneapi.example.com"
			return s
		}(),
		func() ValidationSettings {
			s := DefaultValidationSettings()
			s.Endpoint = "/api/token/balance"
			return s
		}(),
	} {
		require.Error(t, CheckValidationBounds(settings), "%+v", settings)
	}
	legacy := DefaultValidationSettings()
	legacy.Endpoint = "https://proxy.example.com/custom/balance"
	require.NoError(t, CheckValidationBounds(legacy))
}

// TestAuthenticateDecisionMetrics pins one result per Authenticate call, so
// the alerts in docs/manual/mcp.md read what they claim.
func TestAuthenticateDecisionMetrics(t *testing.T) {
	read := func(result string) float64 { return testutil.ToFloat64(authMetrics.decisions.WithLabelValues(result)) }
	authority := &scriptedAuthority{verdict: map[string]error{"sk-known": nil}}
	authenticator, clock := newScriptedAuthenticator(t, authority, AuthenticatorOptions{PositiveTTL: time.Minute, StaleGrace: time.Hour})
	ctx := context.Background()
	step := func(result string, run func()) {
		t.Helper()
		before := read(result)
		run()
		require.InDelta(t, before+1, read(result), 0, result)
	}

	step(decisionAcceptedAuthority, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-known") })
	step(decisionAcceptedCache, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-known") })
	step(decisionRejectedAuthority, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-unknown") })
	step(decisionRejectedCache, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-unknown") })
	step(decisionInvalid, func() { _, _ = authenticator.Authenticate(ctx, "Bearer ") })
	authority.outage.Store(true)
	clock.Advance(2 * time.Minute)
	step(decisionAcceptedStale, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-known") })
	step(decisionUnavailable+UnavailableCauseAuthority, func() { _, _ = authenticator.Authenticate(ctx, "Bearer sk-other") })

	authority.outage.Store(false)
	clock.Advance(unavailableRetry)
	authority.mu.Lock()
	authority.verdict["sk-known"] = ErrCredentialRejected
	authority.mu.Unlock()
	before := testutil.ToFloat64(authMetrics.knownKeyRejections)
	_, err := authenticator.Authenticate(ctx, "Bearer sk-known")
	require.ErrorIs(t, err, ErrCredentialRejected)
	require.InDelta(t, before+1, testutil.ToFloat64(authMetrics.knownKeyRejections), 0,
		"a rejection of a key one-api accepted before is counted for the token-store alert")
}
