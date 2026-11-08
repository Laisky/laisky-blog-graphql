package clientip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	// productionProxyPeer is a Tailscale (CGNAT) address a reverse proxy connects from.
	productionProxyPeer = "100.64.0.10:51234"
	// directPeer is a public caller that reaches the service without a proxy.
	directPeer = "198.51.100.10:40000"
)

// resolvingEngine builds a gin engine with the policy applied and returns the
// address each consumer kind observes: a gin handler, a context-based caller
// (GraphQL resolvers) and a plain net/http handler mounted in gin (MCP).
func resolvingEngine(t *testing.T, policy Policy) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, policy.Apply(engine))
	engine.Use(Middleware())
	engine.GET("/gin", func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c))
	})
	engine.GET("/std", gmw.FromStd(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(FromContext(r.Context())))
	}))
	mounted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(FromRequest(r)))
	})
	engine.GET("/mounted", func(c *gin.Context) { mounted.ServeHTTP(c.Writer, c.Request) })
	return engine
}

// resolve sends one request through engine and returns the address every
// consumer kind saw, failing when they disagree.
func resolve(t *testing.T, engine *gin.Engine, remoteAddr string, headers http.Header) string {
	t.Helper()
	var seen []string
	for _, path := range []string{"/gin", "/std", "/mounted"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = remoteAddr
		for name, values := range headers {
			for _, value := range values {
				request.Header.Add(name, value)
			}
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		seen = append(seen, recorder.Body.String())
	}
	require.Equal(t, seen[0], seen[1], "gin handlers and resolvers must see one identity")
	require.Equal(t, seen[0], seen[2], "gin handlers and mounted net/http handlers must see one identity")
	return seen[0]
}

// TestDefaultPolicyResolution covers the production topology (Cloudflare,
// then nginx on a private or Tailscale address) and the attacks the default
// policy must withstand.
func TestDefaultPolicyResolution(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	engine := resolvingEngine(t, policy)

	cases := []struct {
		name    string
		remote  string
		headers http.Header
		want    string
	}{
		{"direct peer cannot pick its address through X-Forwarded-For", directPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.1"}}, "198.51.100.10"},
		{"direct peer cannot pick its address through X-Real-IP", directPeer,
			http.Header{"X-Real-Ip": {"203.0.113.2"}}, "198.51.100.10"},
		{"direct peer cannot pick its address through both", directPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.3"}, "X-Real-Ip": {"203.0.113.4"}}, "198.51.100.10"},
		{"proxy that overwrites the chain", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.7"}}, "203.0.113.7"},
		{"client-supplied chain passed through and appended by the CDN", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"198.51.100.99, 10.0.0.1, 203.0.113.7"}, "X-Real-Ip": {"198.51.100.98"}}, "203.0.113.7"},
		{"trusted hops are skipped from the right", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.7, 10.0.0.2, 172.17.0.1"}}, "203.0.113.7"},
		{"multiple header lines form one chain", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"198.51.100.1", "203.0.113.7"}}, "203.0.113.7"},
		{"malformed rightmost entry falls back to the peer, never to X-Real-IP", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.7, not-an-ip"}, "X-Real-Ip": {"198.51.100.66"}}, "100.64.0.10"},
		{"malformed entry left of the client is never reached", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"not-an-ip, 203.0.113.7"}}, "203.0.113.7"},
		{"address with a port is malformed", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.7:1234"}}, "100.64.0.10"},
		{"empty list item is malformed", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"203.0.113.7,,"}}, "100.64.0.10"},
		{"X-Real-IP alone is not read by default", productionProxyPeer,
			http.Header{"X-Real-Ip": {"198.51.100.67"}}, "100.64.0.10"},
		{"no forwarding header keeps the peer", productionProxyPeer, nil, "100.64.0.10"},
		{"all-trusted chain yields its leftmost entry", "127.0.0.1:9000",
			http.Header{"X-Forwarded-For": {"10.0.0.3, 10.0.0.2"}}, "10.0.0.3"},
		{"docker bridge gateway is a trusted proxy", "172.17.0.1:5555",
			http.Header{"X-Forwarded-For": {"203.0.113.8"}}, "203.0.113.8"},
		{"IPv6 client behind an IPv6 unique-local proxy is canonicalized", "[fd00::5]:443",
			http.Header{"X-Forwarded-For": {"2001:DB8:0:0::1"}}, "2001:db8::1"},
		{"IPv6 direct peer cannot pick its address", "[2001:db8::9]:443",
			http.Header{"X-Forwarded-For": {"2001:db8::10"}}, "2001:db8::9"},
		{"IPv6 loopback proxy", "[::1]:443",
			http.Header{"X-Forwarded-For": {"203.0.113.11"}}, "203.0.113.11"},
		{"IPv4-mapped client is unmapped", productionProxyPeer,
			http.Header{"X-Forwarded-For": {"::ffff:203.0.113.9"}}, "203.0.113.9"},
		{"unparsable peer is unknown", "garbage", http.Header{"X-Forwarded-For": {"203.0.113.12"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolve(t, engine, tc.remote, tc.headers))
		})
	}
}

// TestExplicitEmptyPolicyIgnoresEveryHeader verifies a direct deployment
// (trusted_proxies: []) identifies clients by the peer alone, even loopback.
func TestExplicitEmptyPolicyIgnoresEveryHeader(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(map[string]any{ConfigKeyTrustedProxies: []any{}}))
	require.NoError(t, err)
	engine := resolvingEngine(t, policy)

	require.Equal(t, "127.0.0.1", resolve(t, engine, "127.0.0.1:1234",
		http.Header{"X-Forwarded-For": {"203.0.113.20"}, "X-Real-Ip": {"203.0.113.21"}}))
	require.Equal(t, "198.51.100.10", resolve(t, engine, directPeer, http.Header{"X-Forwarded-For": {"203.0.113.22"}}))
}

// TestConfiguredPolicyNarrowsTrust verifies an operator can trust exactly one
// proxy and opt into X-Real-IP for an ingress that sets only that header.
func TestConfiguredPolicyNarrowsTrust(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(map[string]any{
		ConfigKeyTrustedProxies: []any{"100.64.0.10"},
		ConfigKeyHeaders:        []any{"X-Real-IP"},
	}))
	require.NoError(t, err)
	engine := resolvingEngine(t, policy)

	require.Equal(t, "203.0.113.30", resolve(t, engine, productionProxyPeer,
		http.Header{"X-Real-Ip": {"203.0.113.30"}, "X-Forwarded-For": {"203.0.113.31"}}))
	require.Equal(t, "10.0.0.9", resolve(t, engine, "10.0.0.9:80", http.Header{"X-Real-Ip": {"203.0.113.32"}}),
		"a private peer outside the configured list is no longer trusted")

	disabled, err := NewPolicy([]string{"100.64.0.10"}, nil)
	require.NoError(t, err)
	require.Equal(t, "100.64.0.10", resolve(t, resolvingEngine(t, disabled), productionProxyPeer,
		http.Header{"X-Forwarded-For": {"203.0.113.33"}}), "empty header list disables forwarding headers")
}

// TestApplyOverridesTrustedPlatform verifies a platform header preset (which
// gin trusts from any peer) cannot survive the policy.
func TestApplyOverridesTrustedPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.TrustedPlatform = gin.PlatformCloudflare
	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	require.NoError(t, policy.Apply(engine))
	require.Empty(t, engine.TrustedPlatform)
	require.Error(t, policy.Apply(nil))
}

// TestFromContextWithoutMiddlewareUsesPeerOnly verifies a request that never
// passed through Attach is identified by its peer, never by a header, even on
// gin's trust-all default engine.
func TestFromContextWithoutMiddlewareUsesPeerOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	gctx.Request = httptest.NewRequest(http.MethodPost, "/query", nil)
	gctx.Request.RemoteAddr = "127.0.0.1:1234"
	gctx.Request.Header.Set("X-Forwarded-For", "203.0.113.40")
	ctx := context.WithValue(context.Background(), gmw.CtxKeyGin, gctx)

	require.Equal(t, "127.0.0.1", FromContext(ctx))
	require.Equal(t, "127.0.0.1", FromContext(gctx))
	require.Equal(t, "127.0.0.1", FromRequest(gctx.Request))
	require.Empty(t, FromContext(context.Background()))
	require.Empty(t, FromContext(nil)) //nolint:staticcheck // SA1012: nil context is a supported input
	require.Empty(t, FromRequest(nil))
	require.Equal(t, "203.0.113.41", FromContext(WithIP(context.Background(), "203.0.113.41")))
	require.Empty(t, Attach(nil))
}

// TestCanonical verifies the canonical textual form of client addresses.
func TestCanonical(t *testing.T) {
	require.Equal(t, "203.0.113.1", Canonical(" 203.0.113.1 "))
	require.Equal(t, "2001:db8::1", Canonical("2001:DB8:0::1"))
	require.Equal(t, "203.0.113.1", Canonical("::ffff:203.0.113.1"))
	require.Equal(t, "fe80::1", Canonical("fe80::1%eth0"))
	require.Empty(t, Canonical("example.com"))
	require.Empty(t, Canonical(""))
	require.Equal(t, "203.0.113.1", PeerAddress("203.0.113.1:80"))
	require.Equal(t, "2001:db8::1", PeerAddress("[2001:db8::1]:80"))
	require.Empty(t, PeerAddress("203.0.113.1"))
}

// TestNetworkKeyGroupsIPv6By64 verifies one host cannot obtain fresh risk
// buckets by rotating addresses inside its /64, while IPv4 stays per address.
func TestNetworkKeyGroupsIPv6By64(t *testing.T) {
	require.Equal(t, "203.0.113.5", NetworkKey("203.0.113.5"))
	require.Equal(t, "203.0.113.5", NetworkKey("::ffff:203.0.113.5"))
	require.Equal(t, "2001:db8:1:2::/64", NetworkKey("2001:db8:1:2::1"))
	require.Equal(t, NetworkKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd"), NetworkKey("2001:db8:1:2::ffff"))
	require.NotEqual(t, NetworkKey("2001:db8:1:2::1"), NetworkKey("2001:db8:1:3::1"))
	require.Equal(t, "fe80::/64", NetworkKey("fe80::1%eth0"))
	require.Empty(t, NetworkKey("unknown"))
}

// TestGinTrustAgreesWithPolicy verifies the engine trusts exactly the peers the
// policy's normalized prefixes contain, including for IPv4-mapped entries that
// gin's net.IPNet matching treats as IPv4 networks.
func TestGinTrustAgreesWithPolicy(t *testing.T) {
	peers := []string{"10.1.2.3", "192.168.1.7", "192.168.1.8", "203.0.113.1", "100.64.0.10", "2001:db8::1", "fd00::1"}
	for _, entries := range [][]string{
		{"::ffff:10.0.0.0/104", "::ffff:192.168.1.7"},
		{"::ffff:0:0/96"},
		{"::/0"},
		{"fd00::/8", "100.64.0.0/10"},
	} {
		policy, err := NewPolicy(entries, DefaultHeaders())
		require.NoError(t, err)
		engine := resolvingEngine(t, policy)
		for _, peer := range peers {
			addr := netip.MustParseAddr(peer)
			want := false
			for _, trusted := range policy.TrustedProxies() {
				want = want || netip.MustParsePrefix(trusted).Contains(addr)
			}

			remote := netip.AddrPortFrom(addr, 4000).String()
			got := resolve(t, engine, remote, http.Header{"X-Forwarded-For": {"198.51.100.200"}}) == "198.51.100.200"
			require.Equal(t, want, got, "entries %v, peer %s: gin trust must match the policy", entries, peer)
		}
	}
}
