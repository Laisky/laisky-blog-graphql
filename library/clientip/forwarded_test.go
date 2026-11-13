package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// forwardedEngine echoes the host and scheme the policy-aware middleware
// believes for each request.
func forwardedEngine(t *testing.T, policy Policy) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	require.NoError(t, policy.Apply(engine))
	engine.Use(policy.Middleware())
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, RequestHost(c.Request)+"|"+ForwardedProto(c.Request)+"|"+FromContext(c))
	})
	return engine
}

// TestForwardedHostOnlyFromTrustedProxy reproduces review finding 1 at the
// header layer: X-Forwarded-Host and X-Forwarded-Proto count only when the
// connection peer is a trusted proxy, and only as plain values.
func TestForwardedHostOnlyFromTrustedProxy(t *testing.T) {
	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	engine := forwardedEngine(t, policy)

	cases := []struct {
		name    string
		remote  string
		headers http.Header
		want    string
	}{
		{"direct peer cannot choose the host or scheme", directPeer,
			http.Header{"X-Forwarded-Host": {"evil.example"}, "X-Forwarded-Proto": {"https"}}, "sso.example.test||198.51.100.10"},
		{"trusted proxy reports host and scheme", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"mcp.example.test"}, "X-Forwarded-Proto": {"HTTPS"}, "X-Forwarded-For": {"203.0.113.5"}},
			"mcp.example.test|https|203.0.113.5"},
		{"the entry the nearest proxy added wins", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"client.example, proxy.example.test"}}, "proxy.example.test||100.64.0.10"},
		{"multiple header lines form one list", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"client.example", "proxy.example.test:8443"}}, "proxy.example.test:8443||100.64.0.10"},
		{"a value with a path falls back to Host", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"evil.example/path"}}, "sso.example.test||100.64.0.10"},
		{"userinfo falls back to Host", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"user@evil.example"}}, "sso.example.test||100.64.0.10"},
		{"an unknown scheme is dropped", productionProxyPeer,
			http.Header{"X-Forwarded-Proto": {"javascript"}}, "sso.example.test||100.64.0.10"},
		{"IPv6 literal host", productionProxyPeer,
			http.Header{"X-Forwarded-Host": {"[2001:db8::1]:443"}}, "[2001:db8::1]:443||100.64.0.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://sso.example.test/", nil)
			request.RemoteAddr = tc.remote
			for name, values := range tc.headers {
				for _, value := range values {
					request.Header.Add(name, value)
				}
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, tc.want, recorder.Body.String())
		})
	}
}

// TestForwardedHostWithoutPolicyMiddleware verifies the package-level Attach
// and a request that skipped the middleware never honor X-Forwarded-Host.
func TestForwardedHostWithoutPolicyMiddleware(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://sso.example.test/", nil)
	request.RemoteAddr = productionProxyPeer
	request.Header.Set("X-Forwarded-Host", "evil.example")
	require.Equal(t, "sso.example.test", RequestHost(request))
	require.Empty(t, ForwardedHost(request))
	require.Empty(t, RequestHost(nil))

	policy, err := LoadPolicy(mapGetter(nil))
	require.NoError(t, err)
	engine := resolvingEngine(t, policy) // package-level Middleware
	engine.GET("/host", func(c *gin.Context) { c.String(http.StatusOK, RequestHost(c.Request)) })
	hostRequest := httptest.NewRequest(http.MethodGet, "http://sso.example.test/host", nil)
	hostRequest.RemoteAddr = productionProxyPeer
	hostRequest.Header.Set("X-Forwarded-Host", "evil.example")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, hostRequest)
	require.Equal(t, "sso.example.test", recorder.Body.String())
}

// TestPolicyTrustsPeerMatchesGin verifies the header policy and gin agree on
// which peers are trusted proxies.
func TestPolicyTrustsPeerMatchesGin(t *testing.T) {
	policy, err := NewPolicy([]string{"::ffff:10.0.0.0/104", "fd00::/8"}, DefaultHeaders())
	require.NoError(t, err)
	require.True(t, policy.TrustsPeer("10.1.2.3:80"))
	require.True(t, policy.TrustsPeer("[::ffff:10.1.2.3]:80"))
	require.True(t, policy.TrustsPeer("[fd00::1]:80"))
	require.False(t, policy.TrustsPeer("192.168.1.1:80"))
	require.False(t, policy.TrustsPeer("garbage"))
}
