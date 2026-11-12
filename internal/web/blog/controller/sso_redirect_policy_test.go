package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const (
	policyTestState       = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	policyTestChallenge   = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA"
	policyTestAccounting  = "https://accounting.laisky.com/api/auth/sso/callback"
	policyTestCodeRequest = policyTestAccounting + "?sso_flow=code&sso_client_id=accounting&sso_state=" + policyTestState +
		"&sso_challenge=" + policyTestChallenge + "&sso_challenge_method=S256"
)

// configureSSORedirectPolicyFixture installs an inert redirect policy and restores the previous settings.
func configureSSORedirectPolicyFixture(t *testing.T, mode string, clients any) {
	t.Helper()
	oldMode := gconfig.Shared.Get("settings.web.sso.legacy_token_redirect")
	oldClients := gconfig.Shared.Get("settings.web.sso.clients")
	oldSecret := gconfig.Shared.GetString("settings.secret")
	gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", mode)
	gconfig.Shared.Set("settings.web.sso.clients", clients)
	gconfig.Shared.Set("settings.secret", "sso-redirect-inert-unit-fixture")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", oldMode)
		gconfig.Shared.Set("settings.web.sso.clients", oldClients)
		gconfig.Shared.Set("settings.secret", oldSecret)
	})
}

// accountingRegistryFixture registers one synthetic client that must use the code flow.
func accountingRegistryFixture() map[string]any {
	return map[string]any{"accounting": map[string]any{"redirect_uris": []any{policyTestAccounting}}}
}

// issuerRequestContext models a GraphQL request addressed to the public SSO origin.
func issuerRequestContext(t *testing.T) context.Context {
	t.Helper()
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	// An absolute https URL gives the request a TLS state, so the scheme does not
	// depend on forwarding headers that only a trusted proxy may supply.
	request := httptest.NewRequest(http.MethodPost, "https://sso.laisky.com/query", nil)
	gctx.Request = request
	return context.WithValue(context.Background(), gmw.CtxKeyGin, gctx)
}

// TestGitHubRedirectTargetPolicy proves the backend start paths apply the shared redirect policy and cutover switch.
func TestGitHubRedirectTargetPolicy(t *testing.T) {
	type expectation struct {
		target string
		allow  bool
		deny   bool
	}
	for _, item := range []expectation{
		{target: "https://cv.laisky.com/cv", allow: true, deny: false},
		{target: "https://chat.laisky.com/gptchat?state=abc", allow: true, deny: false},
		{target: "http://cv.laisky.com/cv", allow: false, deny: false},
		{target: "http://accounting.laisky.com:8080/api/auth/sso/callback", allow: false, deny: false},
		{target: "https://user@cv.laisky.com/cv", allow: false, deny: false},
		{target: "https://cv.laisky.com\\@evil.example/", allow: false, deny: false},
		{target: "https://evil.example/", allow: false, deny: false},
		{target: "http://100.64.0.10:5173/gptchat", allow: false, deny: false},
		{target: "http://localhost:5173/cb", allow: true, deny: false},
		{target: "http://[::ffff:a00:1]/cb", allow: false, deny: false},
		{target: policyTestAccounting + "?state=legacy", allow: false, deny: false},
		{target: policyTestCodeRequest, allow: true, deny: true},
		{target: strings.Replace(policyTestCodeRequest, "/callback?", "/other?", 1), allow: false, deny: false},
		{target: "https://blog.laisky.com/?sso_flow=code&sso_state=" + policyTestState + "&sso_challenge=" + policyTestChallenge +
			"&sso_challenge_method=S256", allow: true, deny: true},
		{target: "http://blog.laisky.com/p/article/", allow: true, deny: true},
		{target: "https://sso.laisky.com/profile", allow: true, deny: true},
		{target: "/profile", allow: true, deny: true},
	} {
		for mode, want := range map[string]bool{"allow": item.allow, "deny": item.deny} {
			t.Run(mode+" "+item.target, func(t *testing.T) {
				configureSSORedirectPolicyFixture(t, mode, accountingRegistryFixture())
				raw := item.target
				_, err := resolveGitHubOAuthRedirectTarget(issuerRequestContext(t), &raw)
				if want {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

// countingTransport records whether a GitHub token exchange reached the inert fixture.
type countingTransport struct{ calls atomic.Int32 }

// RoundTrip answers every request with a synthetic failure and counts it.
func (c *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader("{}")), Request: request}, nil
}

// TestGitHubOAuthLoginRevalidatesRedirect proves a state signed under an older policy cannot complete toward a now-refused target.
func TestGitHubOAuthLoginRevalidatesRedirect(t *testing.T) {
	for _, item := range []struct {
		name, mode, target string
	}{
		{name: "insecure target signed before the https policy", mode: "allow", target: "http://cv.laisky.com/cv"},
		{name: "legacy target after the deny cutover", mode: "deny", target: "https://cv.laisky.com/cv"},
		{name: "registered hostname without a code request", mode: "allow", target: policyTestAccounting + "?state=legacy"},
	} {
		t.Run(item.name, func(t *testing.T) {
			configureSSORedirectPolicyFixture(t, item.mode, accountingRegistryFixture())
			oldID := gconfig.Shared.GetString(githubOAuthClientIDConfigKey)
			oldSecret := gconfig.Shared.GetString(githubOAuthClientSecretConfigKey)
			gconfig.Shared.Set(githubOAuthClientIDConfigKey, "inert-client-id")
			gconfig.Shared.Set(githubOAuthClientSecretConfigKey, "inert-client-secret")
			t.Cleanup(func() {
				gconfig.Shared.Set(githubOAuthClientIDConfigKey, oldID)
				gconfig.Shared.Set(githubOAuthClientSecretConfigKey, oldSecret)
			})
			state, err := signGitHubOAuthState(githubOAuthState{
				RedirectTo:  item.target,
				CallbackURL: "https://sso.laisky.com/github/callback",
				Kind:        githubOAuthStateKindLogin,
				Nonce:       gutils.UUID7(),
				ExpiresAt:   gutils.Clock.GetUTCNow().Add(time.Minute).Unix(),
			})
			require.NoError(t, err)
			transport := new(countingTransport)
			ctx := context.WithValue(issuerRequestContext(t), oauth2.HTTPClient, &http.Client{Transport: transport})

			_, err = new(MutationResolver).UserGithubOAuthLogin(ctx, "inert-code", state)
			require.ErrorContains(t, err, "redirect target")
			require.Zero(t, transport.calls.Load(), "a refused target must stop before the GitHub code exchange")
		})
	}
}

// TestPasskeyFinishRevalidatesRedirect proves a signed passkey session cannot complete toward a now-refused target.
func TestPasskeyFinishRevalidatesRedirect(t *testing.T) {
	for _, item := range []struct {
		name, mode, target string
	}{
		{name: "insecure target signed before the https policy", mode: "allow", target: "http://cv.laisky.com/cv"},
		{name: "legacy target after the deny cutover", mode: "deny", target: "https://cv.laisky.com/cv"},
	} {
		t.Run(item.name, func(t *testing.T) {
			configureSSORedirectPolicyFixture(t, item.mode, nil)
			session, err := signPasskeySession(passkeySessionEnvelope{
				Kind:       passkeySessionKindLogin,
				RedirectTo: item.target,
				ExpiresAt:  gutils.Clock.GetUTCNow().Add(time.Minute).Unix(),
			})
			require.NoError(t, err)

			_, err = new(MutationResolver).UserFinishPasskeyLogin(issuerRequestContext(t), session, "{}")
			require.ErrorContains(t, err, "redirect target")
		})
	}
}

// TestDefaultRedirectOpensCompletingIssuerProfile proves an absent target completes on the
// issuer page that finishes the ceremony, even after the legacy handoff is denied.
func TestDefaultRedirectOpensCompletingIssuerProfile(t *testing.T) {
	configureSSORedirectPolicyFixture(t, "deny", nil)
	ctx := issuerRequestContext(t)
	target, err := resolveGitHubOAuthRedirectTarget(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "/profile", target)
	empty := "  "
	target, err = resolveGitHubOAuthRedirectTarget(ctx, &empty)
	require.NoError(t, err)
	require.Equal(t, "/profile", target)
	require.NoError(t, ensureSSORedirectStillAllowed(ctx, target))
	// A cross-origin issuer page would need a URL bearer, which deny refuses.
	require.Error(t, ensureSSORedirectStillAllowed(ctx, "https://mcp.laisky.com/profile"))
}
