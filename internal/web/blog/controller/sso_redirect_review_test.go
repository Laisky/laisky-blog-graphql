package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/ssoredirect"
)

// requestContextFor models a GraphQL request addressed to origin (scheme and host).
func requestContextFor(t *testing.T, scheme, host string) context.Context {
	t.Helper()
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := httptest.NewRequest(http.MethodPost, "/query", nil)
	request.Host = host
	request.Header.Set("X-Forwarded-Proto", scheme)
	gctx.Request = request
	return context.WithValue(context.Background(), gmw.CtxKeyGin, gctx)
}

// TestPrivateTargetsRequireDevelopmentIssuer proves a production issuer never hands the legacy
// bearer to an unregistered private, CGNAT or ULA target, while loopback and development issuers keep working.
func TestPrivateTargetsRequireDevelopmentIssuer(t *testing.T) {
	configureSSORedirectPolicyFixture(t, "allow", map[string]any{"devapp": map[string]any{
		"redirect_uris": []any{"http://192.168.1.66:8080/sso/callback"}, "legacy_token_redirect": "allow"}})
	production := requestContextFor(t, "https", "sso.laisky.com")
	development := requestContextFor(t, "http", "192.168.1.10:5173")
	for _, item := range []struct {
		target            string
		production, local bool
	}{
		{target: "http://192.168.1.77/x", production: false, local: true},
		{target: "https://10.0.0.5/x", production: false, local: true},
		{target: "http://100.64.0.10:5173/gptchat", production: false, local: true},
		{target: "http://[fd00::1]/cb", production: false, local: true},
		{target: "http://localhost:5173/cb", production: true, local: true},
		{target: "http://127.0.0.1:8080/cb", production: true, local: true},
		{target: "http://[::1]:8080/cb", production: true, local: true},
		{target: "http://192.168.1.66:8080/sso/callback?state=x", production: true, local: true},
		{target: "http://192.168.1.66:8080/other", production: false, local: true},
	} {
		for name, ctx := range map[string]context.Context{"production": production, "development": development} {
			want := item.production
			if name == "development" {
				want = item.local
			}
			raw := item.target
			_, err := resolveGitHubOAuthRedirectTarget(ctx, &raw)
			if want {
				require.NoError(t, err, "%s issuer, target %s", name, raw)
			} else {
				require.Error(t, err, "%s issuer, target %s", name, raw)
			}
		}
	}
}

// TestRegistryRejectsSharedHostnames proves classification cannot depend on client ID order.
func TestRegistryRejectsSharedHostnames(t *testing.T) {
	_, err := ssoredirect.Parse("allow", map[string]any{
		"first":  map[string]any{"redirect_uris": []any{"https://shared.laisky.com/a"}},
		"second": map[string]any{"redirect_uris": []any{"https://shared.laisky.com/b"}},
	})
	require.Error(t, err)
}

// TestBackslashOnlyAfterQueryOrFragment proves browser-built legacy targets keep working while
// authority and path spellings that parsers read differently stay refused.
func TestBackslashOnlyAfterQueryOrFragment(t *testing.T) {
	configureSSORedirectPolicyFixture(t, "allow", nil)
	ctx := issuerRequestContext(t)
	accepted := "https://chat.laisky.com/p?q=a\\b#f\\g"
	target, err := resolveGitHubOAuthRedirectTarget(ctx, &accepted)
	require.NoError(t, err)
	require.Equal(t, accepted, target)
	for _, refused := range []string{"https://evil.example\\@sso.laisky.com/", "https://chat.laisky.com/a\\b?x=1"} {
		_, err := resolveGitHubOAuthRedirectTarget(ctx, &refused)
		require.Error(t, err, refused)
	}
}

// TestGitHubStartChecksTheCompletingIssuer proves a target that is an issuer page only for the
// starting origin is judged against the configured callback origin before the user leaves for GitHub.
func TestGitHubStartChecksTheCompletingIssuer(t *testing.T) {
	for _, item := range []struct {
		name, mode, host, target string
		allowed                  bool
	}{
		{name: "cross-issuer page under deny", mode: "deny", host: "mcp.laisky.com", target: "https://mcp.laisky.com/sso/profile"},
		{name: "relative cross-issuer page under deny", mode: "deny", host: "mcp.laisky.com", target: "/sso/profile"},
		{name: "cross-issuer page under allow", mode: "allow", host: "mcp.laisky.com", target: "https://mcp.laisky.com/sso/profile", allowed: true},
		{name: "completing issuer page under deny", mode: "deny", host: "sso.laisky.com", target: "https://sso.laisky.com/profile", allowed: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			configureSSORedirectPolicyFixture(t, item.mode, nil)
			for key, value := range map[string]string{
				githubOAuthClientIDConfigKey:             "inert-client-id",
				githubOAuthClientSecretConfigKey:         "inert-client-secret",
				"settings.web.github_oauth.redirect_url": "https://sso.laisky.com/github/callback",
			} {
				old := gconfig.Shared.Get(key)
				gconfig.Shared.Set(key, value)
				t.Cleanup(func() { gconfig.Shared.Set(key, old) })
			}
			raw := item.target
			response, err := new(MutationResolver).UserGithubOAuthStart(requestContextFor(t, "https", item.host), &raw, nil)
			if item.allowed {
				require.NoError(t, err)
				require.NotEmpty(t, response.AuthorizeURL)
				return
			}
			require.ErrorContains(t, err, "redirect target")
			require.Nil(t, response, "the user must not be sent to GitHub")
		})
	}
}
