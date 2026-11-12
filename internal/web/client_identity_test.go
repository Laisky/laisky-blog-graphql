package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	blogCtl "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// identityPasskeyStart is a login mutation that consults the adaptive
// authentication tracker without needing a user store.
const identityPasskeyStart = `mutation { UserStartPasskeyLogin { session } }`

// newIdentityEngine builds the production engine (client-identity policy plus
// shared middleware) with the production GraphQL schema mounted at /query, and
// enables Turnstile so every login start is risk-tracked.
func newIdentityEngine(t *testing.T, policyConfig map[string]any) *gin.Engine {
	t.Helper()
	return newIdentityEngineWithTurnstile(t, policyConfig, map[string]any{
		"settings.web.turnstile.secret_key": "identity-test-turnstile-secret",
		"settings.web.turnstile.site_key":   "identity-test-site-key",
		"settings.web.sites":                map[string]any{},
	})
}

// newIdentityEngineWithTurnstile is newIdentityEngine with explicit Turnstile
// site and global settings.
func newIdentityEngineWithTurnstile(t *testing.T, policyConfig map[string]any, turnstile map[string]any) *gin.Engine {
	t.Helper()
	ssoGlobalsMu.Lock()
	t.Cleanup(ssoGlobalsMu.Unlock)
	overrides := map[string]any{
		"settings.secret":              ssoE2ESecret,
		"settings.web.webauthn.origin": ssoE2EOrigin,
		"settings.web.webauthn.rp_id":  ssoE2ERPID,
	}
	for key, value := range turnstile {
		overrides[key] = value
	}
	restoreSSOConfig(t, overrides)

	// Each test starts from an empty process-wide risk tracker, so repeated
	// runs (-count=N) see the same thresholds.
	blogCtl.ResetAuthChallengeTracker()
	t.Cleanup(blogCtl.ResetAuthChallengeTracker)

	policy, err := clientip.LoadPolicy(func(key string) any { return policyConfig[key] })
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	engine, err := newServerEngine(policy, urlPrefixConfig{})
	require.NoError(t, err)
	server := handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: newContractResolver(ResolverArgs{})}))
	engine.Any("/query", ginMw.FromStd(server.ServeHTTP))
	return engine
}

// startPasskeyLogin posts one passkey-login start from remoteAddr with the given
// forwarding headers and returns the first GraphQL error message, or "" when
// the request was admitted.
func startPasskeyLogin(t *testing.T, engine *gin.Engine, remoteAddr string, headers map[string]string) string {
	t.Helper()
	return startPasskeyLoginOn(t, engine, "example.com", remoteAddr, headers)
}

// startPasskeyLoginOn is startPasskeyLogin for a request addressed to host.
func startPasskeyLoginOn(t *testing.T, engine *gin.Engine, host string, remoteAddr string, headers map[string]string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": identityPasskeyStart})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "http://"+host+"/query", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddr
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var response graphQLResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	if len(response.Errors) == 0 {
		require.Contains(t, string(response.Data["UserStartPasskeyLogin"]), "session")
		return ""
	}
	return response.Errors[0].Message
}

// TestServerEngineDirectPeerCannotChooseRiskKey reproduces issue #84 through
// the production engine: a direct, untrusted peer rotating X-Forwarded-For or
// X-Real-IP must still accumulate risk under its own address and be challenged
// once it exceeds the request-frequency threshold (5 per minute by default).
func TestServerEngineDirectPeerCannotChooseRiskKey(t *testing.T) {
	for name, header := range map[string]string{"X-Forwarded-For": "X-Forwarded-For", "X-Real-IP": "X-Real-IP"} {
		t.Run(name, func(t *testing.T) {
			engine := newIdentityEngine(t, nil)
			peer := "198.51.100.11:40000"
			if header == "X-Real-IP" {
				peer = "198.51.100.12:40000"
			}

			var messages []string
			for i := range 8 {
				messages = append(messages, startPasskeyLogin(t, engine, peer, map[string]string{header: fmt.Sprintf("203.0.113.%d", i+1)}))
			}
			require.Equal(t, []string{"", "", "", "", ""}, messages[:5], "requests within the threshold are admitted")
			for i, message := range messages[5:] {
				require.Contains(t, message, "turnstile_required", "request %d must be challenged despite a rotated %s", i+6, header)
			}
		})
	}
}

// TestServerEngineTrustedProxyChains verifies that behind the production proxy
// (a Tailscale peer) distinct clients keep distinct risk keys, an attacker
// prepending entries cannot leave its own key, and IPv6 clients are grouped by
// their /64.
func TestServerEngineTrustedProxyChains(t *testing.T) {
	engine := newIdentityEngine(t, nil)
	const proxy = "100.64.0.10:51234"

	for i := range 10 {
		require.Empty(t, startPasskeyLogin(t, engine, proxy, map[string]string{"X-Forwarded-For": fmt.Sprintf("192.0.2.%d", 100+i)}),
			"distinct clients behind the proxy are independent")
	}

	var attacker []string
	for i := range 7 {
		chain := fmt.Sprintf("198.51.100.%d, 10.9.9.%d, 192.0.2.200", i+1, i+1)
		attacker = append(attacker, startPasskeyLogin(t, engine, proxy, map[string]string{"X-Forwarded-For": chain, "X-Real-IP": fmt.Sprintf("198.51.100.%d", 50+i)}))
	}
	require.Equal(t, []string{"", "", "", "", ""}, attacker[:5])
	require.Contains(t, attacker[5], "turnstile_required", "prepended entries must not give the attacker fresh keys")
	require.Contains(t, attacker[6], "turnstile_required")

	var rotating []string
	for i := range 7 {
		rotating = append(rotating, startPasskeyLogin(t, engine, proxy, map[string]string{"X-Forwarded-For": fmt.Sprintf("2001:db8:77:1::%x", i+1)}))
	}
	require.Equal(t, []string{"", "", "", "", ""}, rotating[:5])
	require.Contains(t, rotating[5], "turnstile_required", "rotating inside one IPv6 /64 must not give fresh keys")
}

// TestServerEngineDirectDeploymentPolicy verifies trusted_proxies: [] makes even
// a loopback peer's forwarding headers irrelevant.
func TestServerEngineDirectDeploymentPolicy(t *testing.T) {
	engine := newIdentityEngine(t, map[string]any{clientip.ConfigKeyTrustedProxies: []any{}})

	var messages []string
	for i := range 6 {
		messages = append(messages, startPasskeyLogin(t, engine, "127.0.0.9:3000", map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", 60+i)}))
	}
	require.Equal(t, []string{"", "", "", "", ""}, messages[:5])
	require.Contains(t, messages[5], "turnstile_required")
}

// TestServerEngineRejectsInvalidPolicyConfig verifies a typo in
// trusted_proxies fails before any engine is built.
func TestServerEngineRejectsInvalidPolicyConfig(t *testing.T) {
	_, err := clientip.LoadPolicy(func(key string) any {
		if key == clientip.ConfigKeyTrustedProxies {
			return []any{"10.0.0.0/8", "nginx.internal"}
		}
		return nil
	})
	require.ErrorContains(t, err, "nginx.internal")

	previous := gconfig.Shared.Get(clientip.ConfigKeyTrustedProxies)
	gconfig.Shared.Set(clientip.ConfigKeyTrustedProxies, []any{"10.0.0.1/8"})
	t.Cleanup(func() { gconfig.Shared.Set(clientip.ConfigKeyTrustedProxies, previous) })
	require.ErrorContains(t, RunServerContext(t.Context(), "127.0.0.1:0", nil), "host bits set",
		"the server must refuse to start instead of trusting a mistyped network")
}

// TestServerEngineMountedHandlersShareIdentity verifies net/http handlers that
// the engine mounts the way the MCP transport is mounted (directly and through
// serveMCPTransportRoot) see the address the policy resolved, not the proxy.
func TestServerEngineMountedHandlersShareIdentity(t *testing.T) {
	engine := newIdentityEngine(t, nil)
	mcpLike := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clientip.FromRequest(r)))
	})
	engine.POST("/mounted", func(ctx *gin.Context) { mcpLike.ServeHTTP(ctx.Writer, ctx.Request) })
	engine.POST("/.well-known/mcp-test", func(ctx *gin.Context) { serveMCPTransportRoot(ctx, mcpLike) })

	for _, path := range []string{"/mounted", "/.well-known/mcp-test"} {
		for remote, want := range map[string]string{"100.64.0.10:5000": "203.0.113.77", "198.51.100.13:5000": "198.51.100.13"} {
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request.RemoteAddr = remote
			request.Header.Set("X-Forwarded-For", "203.0.113.77")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, want, recorder.Body.String(), "path %s from %s", path, remote)
		}
	}
}
