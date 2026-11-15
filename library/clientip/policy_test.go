package clientip

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mapGetter serves configuration values from a flat map keyed by dotted path.
func mapGetter(values map[string]any) ConfigGetter {
	return func(key string) any { return values[key] }
}

// TestLoadPolicyDefaultsWhenUnset verifies an unset configuration trusts only
// loopback, private, carrier-grade NAT and unique-local networks and reads only
// X-Forwarded-For.
func TestLoadPolicyDefaultsWhenUnset(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	require.Equal(t, []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7",
	}, policy.TrustedProxies())
	require.Equal(t, []string{"X-Forwarded-For"}, policy.Headers())
	require.False(t, policy.TrustsEveryPeer())
}

// TestLoadPolicyExplicitEmptyTrustsNoProxy verifies an explicit empty list
// means a direct deployment where no peer may assert a client address.
func TestLoadPolicyExplicitEmptyTrustsNoProxy(t *testing.T) {
	for name, value := range map[string]any{
		"yaml sequence": []any{},
		"string slice":  []string{},
		"empty string":  "",
	} {
		t.Run(name, func(t *testing.T) {
			policy, err := LoadPolicy(mapGetter(map[string]any{ConfigKeyTrustedProxies: value}))
			require.NoError(t, err)
			require.Empty(t, policy.TrustedProxies())
		})
	}
}

// TestLoadPolicyParsesConfiguredEntries verifies addresses become host
// prefixes, IPv4-mapped IPv6 is unmapped, duplicates collapse and the string
// form used by environment overrides is accepted.
func TestLoadPolicyParsesConfiguredEntries(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(map[string]any{
		ConfigKeyTrustedProxies: []any{" 100.64.0.10 ", "172.17.0.0/16", "::ffff:10.1.2.3", "2001:db8::1", "172.17.0.0/16"},
		ConfigKeyHeaders:        []any{"x-forwarded-for", "X-REAL-IP"},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"100.64.0.10/32", "172.17.0.0/16", "10.1.2.3/32", "2001:db8::1/128"}, policy.TrustedProxies())
	require.Equal(t, []string{"X-Forwarded-For", "X-Real-Ip"}, policy.Headers())

	policy, err = LoadPolicy(mapGetter(map[string]any{ConfigKeyTrustedProxies: "10.0.0.1, 10.0.0.2 192.168.1.0/24"}))
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1/32", "10.0.0.2/32", "192.168.1.0/24"}, policy.TrustedProxies())
}

// TestLoadPolicyRejectsInvalidEntries verifies every malformed entry fails
// with an error naming it rather than being skipped or trusting more.
func TestLoadPolicyRejectsInvalidEntries(t *testing.T) {
	cases := map[string]struct {
		values map[string]any
		want   string
	}{
		"not an address":   {map[string]any{ConfigKeyTrustedProxies: []any{"10.0.0.1", "proxy.internal"}}, `"proxy.internal"`},
		"bad cidr":         {map[string]any{ConfigKeyTrustedProxies: []any{"10.0.0.0/33"}}, `"10.0.0.0/33"`},
		"host bits set":    {map[string]any{ConfigKeyTrustedProxies: []any{"10.0.0.1/8"}}, `"10.0.0.0/8"`},
		"mapped too short": {map[string]any{ConfigKeyTrustedProxies: []any{"::ffff:0:0/80"}}, "shorter than /96"},
		"mapped host bits": {map[string]any{ConfigKeyTrustedProxies: []any{"::ffff:10.0.0.1/104"}}, `"10.0.0.0/8"`},
		"empty entry":      {map[string]any{ConfigKeyTrustedProxies: []any{" "}}, "empty entry"},
		"ipv6 zone":        {map[string]any{ConfigKeyTrustedProxies: []any{"fe80::1%eth0"}}, "zone"},
		"non-string item":  {map[string]any{ConfigKeyTrustedProxies: []any{"10.0.0.1", 42}}, "[1] must be a string"},
		"wrong type":       {map[string]any{ConfigKeyTrustedProxies: map[string]any{"a": "b"}}, "must be a list"},
		"bad header name":  {map[string]any{ConfigKeyHeaders: []any{"X-Forwarded-For:"}}, `"X-Forwarded-For:"`},
		"empty header":     {map[string]any{ConfigKeyHeaders: []any{""}}, "not a valid header name"},
		"wrong header typ": {map[string]any{ConfigKeyHeaders: true}, "must be a list"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadPolicy(mapGetter(tc.values))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	_, err := LoadPolicy(nil)
	require.Error(t, err)
}

// TestPolicyTrustsEveryPeer verifies an operator who explicitly trusts the
// whole address space is detected so startup can warn about it.
func TestPolicyTrustsEveryPeer(t *testing.T) {
	policy, err := NewPolicy([]string{"0.0.0.0/0"}, DefaultHeaders())
	require.NoError(t, err)
	require.True(t, policy.TrustsEveryPeer())

	policy, err = NewPolicy([]string{"::/0"}, DefaultHeaders())
	require.NoError(t, err)
	require.True(t, policy.TrustsEveryPeer())

	// gin matches an IPv4-mapped /96 against every IPv4 peer, so the policy
	// must report it (and the startup warning fire) exactly like 0.0.0.0/0.
	policy, err = NewPolicy([]string{"::ffff:0:0/96"}, DefaultHeaders())
	require.NoError(t, err)
	require.Equal(t, []string{"0.0.0.0/0"}, policy.TrustedProxies())
	require.True(t, policy.TrustsEveryPeer())
}

// TestLoadPolicyNormalizesIPv4MappedPrefixes verifies an IPv4-mapped prefix
// of /96 or longer becomes the IPv4 prefix it covers, the same entry as its
// IPv4 spelling, and a mapped bare address the same as the IPv4 address.
func TestLoadPolicyNormalizesIPv4MappedPrefixes(t *testing.T) {
	mapped, err := NewPolicy([]string{"::ffff:10.0.0.0/104", "::ffff:192.168.1.7/128", "::ffff:172.16.0.9"}, DefaultHeaders())
	require.NoError(t, err)
	plain, err := NewPolicy([]string{"10.0.0.0/8", "192.168.1.7/32", "172.16.0.9"}, DefaultHeaders())
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8", "192.168.1.7/32", "172.16.0.9/32"}, mapped.TrustedProxies())
	require.Equal(t, plain.TrustedProxies(), mapped.TrustedProxies())
	require.False(t, mapped.TrustsEveryPeer())

	deduped, err := NewPolicy([]string{"10.0.0.0/8", "::ffff:10.0.0.0/104"}, DefaultHeaders())
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8"}, deduped.TrustedProxies())
}

// TestDefaultsAreCopies verifies callers cannot mutate the shared defaults.
func TestDefaultsAreCopies(t *testing.T) {
	proxies := DefaultTrustedProxies()
	proxies[0] = "0.0.0.0/0"
	require.Equal(t, "127.0.0.0/8", DefaultTrustedProxies()[0])

	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	headers := policy.Headers()
	headers[0] = "X-Real-Ip"
	require.Equal(t, []string{"X-Forwarded-For"}, policy.Headers())
}

// TestPolicyStartupWarning verifies the operator is warned when the policy
// lets callers outside the reverse proxy choose their address: trusting every
// peer, or relying on the private-network default instead of exact proxy
// addresses. Exact proxies, no proxies, or no forwarding headers are quiet.
func TestPolicyStartupWarning(t *testing.T) {
	load := func(values map[string]any) Policy {
		t.Helper()
		policy, err := LoadPolicy(func(key string) any { return values[key] })
		require.NoError(t, err)
		return policy
	}

	unset := load(nil)
	require.True(t, unset.UsesDefaultTrustedProxies())
	require.Contains(t, unset.StartupWarning(), ConfigKeyTrustedProxies)

	everyone := load(map[string]any{ConfigKeyTrustedProxies: []any{"0.0.0.0/0"}})
	require.Contains(t, everyone.StartupWarning(), "every peer")

	exact := load(map[string]any{ConfigKeyTrustedProxies: []any{"10.0.0.5", "172.17.0.1"}})
	require.False(t, exact.UsesDefaultTrustedProxies())
	require.Empty(t, exact.StartupWarning())

	require.Empty(t, load(map[string]any{ConfigKeyTrustedProxies: []any{}}).StartupWarning(), "direct deployments trust no proxy")

	// Disabling the client-address headers does not make broad trust safe:
	// trusted peers still choose X-Forwarded-Host and X-Forwarded-Proto.
	require.Contains(t, load(map[string]any{ConfigKeyHeaders: []any{}}).StartupWarning(), ConfigKeyTrustedProxies)
	require.Contains(t, load(map[string]any{ConfigKeyHeaders: []any{}, ConfigKeyTrustedProxies: []any{"::/0"}}).StartupWarning(), "every peer")
}
