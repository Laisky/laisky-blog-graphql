package toolpolicy

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// staticResolver returns fixed addresses so an egress decision is asserted
// against a known DNS answer rather than the live internet.
func staticResolver(addresses ...string) ipLookup {
	return func(context.Context, string) ([]net.IPAddr, error) {
		out := make([]net.IPAddr, 0, len(addresses))
		for _, address := range addresses {
			out = append(out, net.IPAddr{IP: net.ParseIP(address)})
		}
		return out, nil
	}
}

// TestAdmitFetchURLPinsResolvedAddresses is the first half of the G05 contract:
// admission must publish the exact addresses it validated, so the renderer can
// connect to those and only those. Returning a bare "allowed" verdict leaves
// the renderer free to resolve the host again and reach a different address,
// which is precisely the DNS-rebinding window.
func TestAdmitFetchURLPinsResolvedAddresses(t *testing.T) {
	t.Parallel()

	t.Run("a resolved host publishes its admitted addresses", func(t *testing.T) {
		t.Parallel()
		admission, err := admitFetchURL(context.Background(), "https://example.com/page?token=secret",
			staticResolver("93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"))
		require.NoError(t, err)
		require.Equal(t, "example.com", admission.Host)
		require.Equal(t, []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}, admission.Addresses)
		require.Equal(t, 443, admission.Port)
		// The pinned set is the admission record, not a log field: it keeps the
		// full target, while the log copy still drops the path token.
		require.Equal(t, "https://example.com", URLForLog(admission.URL))
	})

	t.Run("an IP literal pins itself", func(t *testing.T) {
		t.Parallel()
		admission, err := admitFetchURL(context.Background(), "http://8.8.8.8:8080/x", staticResolver())
		require.NoError(t, err)
		require.Equal(t, []string{"8.8.8.8"}, admission.Addresses)
		require.Equal(t, 8080, admission.Port)
	})

	t.Run("a partially private answer is rejected whole", func(t *testing.T) {
		t.Parallel()
		_, err := admitFetchURL(context.Background(), "https://split-horizon.example",
			staticResolver("93.184.216.34", "127.0.0.1"))
		require.Error(t, err)
	})

	t.Run("every rejection reason keeps producing no admission", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{
			"file:///etc/passwd", "http://localhost/x", "http://169.254.169.254/latest",
			"http://2130706433/", "https://user:pw@example.com/",
		} {
			admission, err := admitFetchURL(context.Background(), raw, staticResolver("93.184.216.34"))
			require.Error(t, err, raw)
			require.Empty(t, admission.Addresses, raw)
		}
	})
}

// TestVerifyEgressChain is the second half: whatever the renderer reports back
// must be re-admitted here. A redirect the renderer followed to a private
// address, or one hop beyond the allowed budget, must fail closed even though
// the original target passed admission.
func TestVerifyEgressChain(t *testing.T) {
	t.Parallel()

	policy := EgressPolicy{
		Host:         "example.com",
		Addresses:    []string{"93.184.216.34"},
		MaxRedirects: 2,
	}

	t.Run("a chain that stays on the admitted origin verifies", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "https://example.com/b"},
			staticResolver("93.184.216.34")))
	})

	t.Run("a redirect into a private address fails closed", func(t *testing.T) {
		t.Parallel()
		err := verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "http://127.0.0.1/admin"},
			staticResolver("93.184.216.34"))
		require.Error(t, err)
	})

	t.Run("a redirect to a host resolving off the pinned set fails closed", func(t *testing.T) {
		t.Parallel()
		err := verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "https://example.com/b"},
			staticResolver("10.0.0.5"))
		require.Error(t, err, "the same host resolving to a private address is the rebinding case")
	})

	t.Run("more hops than the policy allows fails closed", func(t *testing.T) {
		t.Parallel()
		err := verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "https://example.com/b", "https://example.com/c", "https://example.com/d"},
			staticResolver("93.184.216.34"))
		require.Error(t, err)
	})

	t.Run("an empty chain is unverified, not verified", func(t *testing.T) {
		t.Parallel()
		err := verifyEgressChain(context.Background(), policy, nil, staticResolver("93.184.216.34"))
		require.ErrorIs(t, err, ErrEgressUnverified)
	})

	t.Run("a cross-origin redirect is re-admitted on its own addresses", func(t *testing.T) {
		t.Parallel()
		// A different host is allowed only when it independently passes
		// admission; the pinned set belongs to the original host.
		require.NoError(t, verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "https://other.example/b"},
			staticResolver("93.184.216.34")))
		require.Error(t, verifyEgressChain(context.Background(), policy,
			[]string{"https://example.com/a", "https://other.example/b"},
			staticResolver("192.168.1.1")))
	})
}

// TestEgressPolicyFromAdmission keeps the policy handed to the renderer equal
// to what admission actually decided.
func TestEgressPolicyFromAdmission(t *testing.T) {
	t.Parallel()
	admission, err := admitFetchURL(context.Background(), "https://example.com/doc",
		staticResolver("93.184.216.34"))
	require.NoError(t, err)

	policy := admission.Policy(3, false)
	require.Equal(t, "example.com", policy.Host)
	require.Equal(t, admission.Addresses, policy.Addresses)
	require.Equal(t, 3, policy.MaxRedirects)
	require.False(t, policy.AllowSubresources)

	// The policy must be self-contained: a renderer that honors only these
	// fields still cannot reach a non-public address.
	for _, address := range policy.Addresses {
		parsed, parseErr := netip.ParseAddr(address)
		require.NoError(t, parseErr)
		require.True(t, publicAddress(parsed))
	}
}
