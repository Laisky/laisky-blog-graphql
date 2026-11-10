package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// setTurnstileConfig replaces the site and global Turnstile settings for one test.
func setTurnstileConfig(t *testing.T, sites map[string]any, globalSecret string, globalSiteKey string) {
	t.Helper()
	for key, value := range map[string]any{
		"settings.web.sites":                sites,
		"settings.web.turnstile.secret_key": globalSecret,
		"settings.web.turnstile.site_key":   globalSiteKey,
	} {
		previous := gconfig.Shared.Get(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}
}

// TestResolveTurnstileSiteNeverDisablesTheGate reproduces review finding 1 at
// the selection layer: once any secret is configured, no request host may
// select "no secret", and the widget key always comes with its own secret.
func TestResolveTurnstileSiteNeverDisablesTheGate(t *testing.T) {
	exampleSites := map[string]any{
		"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp"},
		"sso": map[string]any{"host": "sso.laisky.com", "router": "sso", "turnstile_site_key": "sso-key", "turnstile_secret_key": "sso-secret"},
	}
	productionSites := map[string]any{
		"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp"},
		"sso": map[string]any{"host": "sso.laisky.com", "router": "sso", "default": true, "turnstile_site_key": "sso-key", "turnstile_secret_key": "sso-secret"},
	}
	noSecretSites := map[string]any{
		"mcp": map[string]any{"host": "mcp.laisky.com", "router": "mcp", "turnstile_site_key": "lonely-key"},
	}
	twoSites := map[string]any{
		"a": map[string]any{"host": "a.example", "turnstile_site_key": "a-key", "turnstile_secret_key": "a-secret"},
		"b": map[string]any{"host": "b.example", "turnstile_site_key": "b-key", "turnstile_secret_key": "b-secret"},
	}
	cases := []struct {
		name         string
		sites        map[string]any
		globalSecret string
		globalKey    string
		host         string
		want         turnstileSite
	}{
		{"example config, own host", exampleSites, "", "", "sso.laisky.com", turnstileSite{"sso-key", "sso-secret"}},
		{"example config, keyless alias host", exampleSites, "", "", "mcp.laisky.com", turnstileSite{"sso-key", "sso-secret"}},
		{"example config, unknown host", exampleSites, "", "", "nope.invalid", turnstileSite{"sso-key", "sso-secret"}},
		{"example config, no host", exampleSites, "", "", "", turnstileSite{"sso-key", "sso-secret"}},
		{"production config, keyless host", productionSites, "", "", "mcp.laisky.com", turnstileSite{"sso-key", "sso-secret"}},
		{"two sites, own host", twoSites, "", "", "b.example", turnstileSite{"b-key", "b-secret"}},
		{"two sites, unknown host stays gated", twoSites, "", "", "c.example", turnstileSite{"a-key", "a-secret"}},
		{"two sites with global pair", twoSites, "g-secret", "g-key", "c.example", turnstileSite{"g-key", "g-secret"}},
		{"global only", map[string]any{}, "g-secret", "", "x.example", turnstileSite{"", "g-secret"}},
		{"nothing configured", noSecretSites, "", "", "x.example", turnstileSite{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setTurnstileConfig(t, tc.sites, tc.globalSecret, tc.globalKey)
			require.Equal(t, tc.want, resolveTurnstileSite(tc.host))
			require.Equal(t, tc.want.siteKey, TurnstileSiteKeyForHost(tc.host))
		})
	}
}

// TestAdmitAuthRequestAccountEscalationNeedsPresentableChallenge verifies the
// account signal demands a challenge only where one can be presented.
func TestAdmitAuthRequestAccountEscalationNeedsPresentableChallenge(t *testing.T) {
	hotAccount := func(t *testing.T) *authChallengeTracker {
		t.Helper()
		tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
		for i := range 3 {
			tracker.recordFailure(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "victim@example.com"))
		}
		withAuthChallengeTracker(t, tracker)
		return tracker
	}
	owner := clientip.WithIP(context.Background(), "192.0.2.50")

	t.Run("token-capable flow with a site key is challenged", func(t *testing.T) {
		setTurnstileEnabledSite(t)
		hotAccount(t)
		_, err := admitAuthRequest(owner, nil, loginFlow("victim@example.com"))
		require.ErrorIs(t, err, model.ErrTurnstileRequired)
	})
	t.Run("legacy flow without a token is not denied by the account", func(t *testing.T) {
		setTurnstileEnabledSite(t)
		hotAccount(t)
		attempt, err := admitAuthRequest(owner, nil, authFlow{action: authActionLogin, account: "victim@example.com"})
		require.NoError(t, err)
		attempt.finish()
	})
	t.Run("pair without a site key is not denied by the account", func(t *testing.T) {
		setTurnstileConfig(t, map[string]any{}, "global-secret", "")
		hotAccount(t)
		attempt, err := admitAuthRequest(owner, nil, loginFlow("victim@example.com"))
		require.NoError(t, err)
		attempt.finish()
	})
	t.Run("failures from the legacy flow still count for the others", func(t *testing.T) {
		setTurnstileEnabledSite(t)
		tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
		withAuthChallengeTracker(t, tracker)
		for i := range 3 {
			ctx := clientip.WithIP(context.Background(), fmt.Sprintf("203.0.113.%d", 10+i))
			attempt, err := admitAuthRequest(ctx, nil, authFlow{action: authActionLogin, account: "victim@example.com"})
			require.NoError(t, err)
			attempt.failed("")
		}
		_, err := admitAuthRequest(owner, nil, loginFlow("victim@example.com"))
		require.ErrorIs(t, err, model.ErrTurnstileRequired)
	})
}

// TestAdmitAuthRequestReleasesOnEveryOutcome verifies an attempt settles once
// and holds no reservation afterwards, whichever way it ends.
func TestAdmitAuthRequestReleasesOnEveryOutcome(t *testing.T) {
	setTurnstileEnabledSite(t)
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	withAuthChallengeTracker(t, tracker)
	ctx := clientip.WithIP(context.Background(), "192.0.2.60")

	for _, settle := range []func(*authAttempt){
		func(a *authAttempt) { a.finish() },
		func(a *authAttempt) { a.succeeded("") },
		func(a *authAttempt) { a.failed(""); a.finish() },
	} {
		attempt, err := admitAuthRequest(ctx, nil, loginFlow("someone@example.com"))
		require.NoError(t, err)
		settle(attempt)
		attempt.finish()
		for _, account := range tracker.accounts {
			require.Zero(t, account.inflight)
		}
	}
	require.Len(t, tracker.accounts, 1, "only the failure is kept")
}

// TestResolveRequestBaseURLHonorsOnlyTrustedForwarding verifies the redirect
// and WebAuthn origin derived from the request believe X-Forwarded-Host and
// X-Forwarded-Proto only from a trusted proxy.
func TestResolveRequestBaseURLHonorsOnlyTrustedForwarding(t *testing.T) {
	policy, err := clientip.LoadPolicy(func(string) any { return nil })
	require.NoError(t, err)
	build := func(remote string) context.Context {
		gin.SetMode(gin.TestMode)
		gctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		gctx.Request = httptest.NewRequest(http.MethodPost, "http://sso.example.test/query", nil)
		gctx.Request.RemoteAddr = remote
		gctx.Request.Header.Set("X-Forwarded-Host", "evil.example")
		gctx.Request.Header.Set("X-Forwarded-Proto", "https")
		policy.Attach(gctx)
		return context.WithValue(context.Background(), ginMw.CtxKeyGin, gctx)
	}

	require.Equal(t, "http://sso.example.test", resolveRequestBaseURL(build("198.51.100.9:1")))
	require.Equal(t, "sso.example.test", resolveTurnstileRequestHost(build("198.51.100.9:1")))
	require.Equal(t, "https://evil.example", resolveRequestBaseURL(build("100.64.0.10:1")),
		"a trusted proxy's report is believed; nginx must set it rather than relay the client's")
}
