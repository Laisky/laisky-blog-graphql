package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	clientFixtureID       = "accounting"
	clientFixtureCallback = "https://accounting.laisky.com/api/auth/sso/callback"
	clientFixtureOrigin   = "https://accounting.laisky.com"
)

// configureSSOClientRegistryFixture registers synthetic code-flow clients and restores the previous registry.
func configureSSOClientRegistryFixture(t *testing.T, clients any) {
	t.Helper()
	old := gconfig.Shared.Get("settings.web.sso.clients")
	gconfig.Shared.Set("settings.web.sso.clients", clients)
	t.Cleanup(func() { gconfig.Shared.Set("settings.web.sso.clients", old) })
}

// TestSSOCodeRegisteredClient proves a registered client receives only a bound, single-use code
// and that its code cannot be redeemed under another client, callback, origin or verifier.
func TestSSOCodeRegisteredClient(t *testing.T) {
	configureSSOCodeFixture(t)
	configureSSOClientRegistryFixture(t, map[string]any{
		clientFixtureID: map[string]any{"redirect_uris": []any{clientFixtureCallback}},
		"cv":            map[string]any{"redirect_uris": []any{"https://cv.laisky.com/cv/sso/callback"}},
	})
	store := miniredis.RunT(t)
	db := redis.NewClient(&redis.Options{Addr: store.Addr()})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var activeChecks atomic.Int32
	handler := ssoCodeHandler{db: db, validate: func(_ *gin.Context, token string) (time.Time, error) {
		activeChecks.Add(1)
		return validateSSOCodeToken(token)
	}}
	router := gin.New()
	router.Use(ssoCodeTransport(urlPrefixConfig{}), allowCORS)
	router.POST("/sso/code", handler.issue)
	router.POST("/sso/token", handler.redeem)
	token := signSSOCodeFixture(t, nil)
	verifier := strings.Repeat("v", 43)
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	bindings := ssoCodeBindings{ClientID: clientFixtureID, RedirectURI: clientFixtureCallback, State: state,
		Challenge: ssoDigest(verifier), ChallengeMethod: "S256"}

	issue := func(t *testing.T) ssoTokenRequest {
		t.Helper()
		result := ssoFixtureRequest(router, "/sso/code", bindings, token, "https://sso.laisky.com")
		require.Equal(t, http.StatusOK, result.Code)
		require.NotContains(t, result.Body.String(), token)
		var body struct {
			Code    string `json:"code"`
			Expires int    `json:"expires_in"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, validSSOEntropy(body.Code))
		require.LessOrEqual(t, body.Expires, 60)
		require.False(t, store.Exists("sso:blog:code:"+ssoDigest(body.Code)), "client codes never share the Blog namespace")
		return ssoTokenRequest{ClientID: clientFixtureID, RedirectURI: clientFixtureCallback, Code: body.Code, State: state, Verifier: verifier}
	}

	t.Run("redeems once from the client origin", func(t *testing.T) {
		grant := issue(t)
		for _, change := range []func(*ssoTokenRequest){
			func(r *ssoTokenRequest) { r.ClientID = "cv" },
			func(r *ssoTokenRequest) { r.ClientID = blogSSOClient },
			func(r *ssoTokenRequest) { r.RedirectURI = "https://cv.laisky.com/cv/sso/callback" },
			func(r *ssoTokenRequest) { r.RedirectURI = clientFixtureCallback + "/" },
			func(r *ssoTokenRequest) { r.State = strings.Repeat("A", 43) },
			func(r *ssoTokenRequest) { r.Verifier = strings.Repeat("w", 43) },
		} {
			invalid := grant
			change(&invalid)
			require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", invalid, "", "").Code)
		}
		// Another registered client's browser origin can never redeem this client's code.
		require.Equal(t, http.StatusForbidden, ssoFixtureRequest(router, "/sso/token", grant, "", "https://cv.laisky.com").Code)
		require.Equal(t, http.StatusForbidden, ssoFixtureRequest(router, "/sso/token", grant, "", blogSSOOrigin+".evil.test").Code)

		result := ssoFixtureRequest(router, "/sso/token", grant, "", clientFixtureOrigin)
		require.Equal(t, http.StatusOK, result.Code)
		require.Equal(t, clientFixtureOrigin, result.Header().Get("Access-Control-Allow-Origin"))
		var body struct {
			Token string `json:"access_token"`
			Type  string `json:"token_type"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, body.Token == token)
		require.Equal(t, "Bearer", body.Type)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", grant, "", clientFixtureOrigin).Code)
	})

	t.Run("server-side redemption without an origin", func(t *testing.T) {
		grant := issue(t)
		require.Equal(t, http.StatusOK, ssoFixtureRequest(router, "/sso/token", grant, "", "").Code)
	})

	t.Run("issuance requires an exact registration", func(t *testing.T) {
		before := activeChecks.Load()
		for _, change := range []func(*ssoCodeBindings){
			func(b *ssoCodeBindings) { b.ClientID = "unknown" },
			func(b *ssoCodeBindings) { b.RedirectURI = clientFixtureCallback + "/" },
			func(b *ssoCodeBindings) { b.RedirectURI = "https://ACCOUNTING.laisky.com/api/auth/sso/callback" },
			func(b *ssoCodeBindings) { b.RedirectURI = "http://accounting.laisky.com/api/auth/sso/callback" },
			func(b *ssoCodeBindings) { b.RedirectURI = clientFixtureCallback + "?x=1" },
			func(b *ssoCodeBindings) { b.RedirectURI = "https://cv.laisky.com/cv/sso/callback" },
			func(b *ssoCodeBindings) { b.ChallengeMethod = "plain" },
			func(b *ssoCodeBindings) { b.ClientID, b.RedirectURI = blogSSOClient, clientFixtureCallback },
		} {
			invalid := bindings
			change(&invalid)
			require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/code", invalid, token, "").Code)
		}
		require.Equal(t, before, activeChecks.Load(), "invalid bindings never reach session validation")
	})

	t.Run("expires in sixty seconds", func(t *testing.T) {
		grant := issue(t)
		store.FastForward(60 * time.Second)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", grant, "", "").Code)
	})

	t.Run("blog keeps its exact built-in binding", func(t *testing.T) {
		blog := ssoCodeBindings{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, State: state,
			Challenge: ssoDigest(verifier), ChallengeMethod: "S256"}
		result := ssoFixtureRequest(router, "/sso/code", blog, token, "https://sso.laisky.com")
		require.Equal(t, http.StatusOK, result.Code)
		var body struct {
			Code string `json:"code"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, store.Exists("sso:blog:code:"+ssoDigest(body.Code)))
		grant := ssoTokenRequest{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, Code: body.Code, State: state, Verifier: verifier}
		require.Equal(t, http.StatusForbidden, ssoFixtureRequest(router, "/sso/token", grant, "", clientFixtureOrigin).Code)
		require.Equal(t, http.StatusOK, ssoFixtureRequest(router, "/sso/token", grant, "", blogSSOOrigin).Code)
	})
}

// TestRuntimeSSORedirectPolicyPayload pins the runtime-config shape the issuer frontend parses and its fail-closed fallback.
func TestRuntimeSSORedirectPolicyPayload(t *testing.T) {
	configureSSOCodeFixture(t)
	oldMode := gconfig.Shared.Get("settings.web.sso.legacy_token_redirect")
	t.Cleanup(func() { gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", oldMode) })
	gctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", nil)
	configureSSOClientRegistryFixture(t, nil)
	raw, err := json.Marshal(loadSSORedirectPolicy(gctx))
	require.NoError(t, err)
	require.JSONEq(t, `{"legacy_token_redirect":"allow","clients":[]}`, string(raw), "the default keeps the legacy handoff")

	gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", "deny")
	configureSSOClientRegistryFixture(t, map[string]any{clientFixtureID: map[string]any{"redirect_uris": []any{clientFixtureCallback}}})
	raw, err = json.Marshal(loadSSORedirectPolicy(gctx))
	require.NoError(t, err)
	require.JSONEq(t, `{"legacy_token_redirect":"deny","clients":[{"client_id":"accounting",`+
		`"redirect_uris":["https://accounting.laisky.com/api/auth/sso/callback"],"legacy_token_redirect":"deny"}]}`, string(raw))

	gconfig.Shared.Set("settings.web.sso.legacy_token_redirect", "sometimes")
	raw, err = json.Marshal(loadSSORedirectPolicy(gctx))
	require.NoError(t, err)
	require.JSONEq(t, `{"legacy_token_redirect":"deny","clients":[]}`, string(raw), "an invalid policy is published fail-closed")
}
