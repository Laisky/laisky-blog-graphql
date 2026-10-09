package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	jwtLib "github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

const ssoTestUID = "12345678-1234-4234-8234-123456789abc"

// configureSSOCodeFixture uses only an inert local signing secret and restores process configuration.
func configureSSOCodeFixture(t *testing.T) {
	t.Helper()
	ssoGlobalsMu.Lock()
	t.Cleanup(ssoGlobalsMu.Unlock)
	oldSecret := gconfig.Shared.GetString("settings.secret")
	oldKey := gconfig.Shared.GetString("settings.web.sso_jwt.private_key")
	gconfig.Shared.Set("settings.secret", "sso-code-inert-unit-fixture-not-a-production-secret")
	gconfig.Shared.Set("settings.web.sso_jwt.private_key", "")
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.secret", oldSecret)
		gconfig.Shared.Set("settings.web.sso_jwt.private_key", oldKey)
	})
}

// signSSOCodeFixture signs local synthetic claims without printing any bearer.
func signSSOCodeFixture(t *testing.T, change func(*jwt.UserClaims)) string {
	t.Helper()
	claims := &jwt.UserClaims{RegisteredClaims: jwtLib.RegisteredClaims{
		Issuer: jwt.SSOIssuer, Subject: ssoTestUID, ExpiresAt: jwtLib.NewNumericDate(time.Now().Add(time.Hour))}, UID: ssoTestUID}
	if change != nil {
		change(claims)
	}
	token, err := jwt.SignSSOToken(claims)
	require.NoError(t, err)
	return token
}

// ssoFixtureRouter mounts scoped handlers with the same transport middleware as production.
func ssoFixtureRouter(handler ssoCodeHandler, prefix urlPrefixConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ssoCodeTransport(prefix))
	for _, path := range ssoRoutePaths(prefix, "/sso/code") {
		router.POST(path, handler.issue)
	}
	for _, path := range ssoRoutePaths(prefix, "/sso/token") {
		router.POST(path, handler.redeem)
	}
	return router
}

// ssoDeadlineRecorder models deadline support only for finite in-memory unit requests.
type ssoDeadlineRecorder struct{ *httptest.ResponseRecorder }

// SetReadDeadline accepts the unit fixture deadline; the socket regression tests actual net/http enforcement.
func (ssoDeadlineRecorder) SetReadDeadline(time.Time) error { return nil }

// ssoFixtureRequest sends bounded local JSON and optional synthetic authorization/origin.
func ssoFixtureRequest(router *gin.Engine, path string, body any, token, origin string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(ssoDeadlineRecorder{response}, request)
	return response
}

// TestSSOCodeLifecycle proves expiry, binding-before-burn, single use, revalidation and replay concurrency in Redis.
func TestSSOCodeLifecycle(t *testing.T) {
	configureSSOCodeFixture(t)
	store := miniredis.RunT(t)
	db := redis.NewClient(&redis.Options{Addr: store.Addr()})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var inactive atomic.Bool
	var expired atomic.Bool
	handler := ssoCodeHandler{db: db, validate: func(_ *gin.Context, token string) (time.Time, error) {
		if inactive.Load() {
			return time.Time{}, errors.New("inactive local fixture")
		}
		if expired.Load() {
			return time.Now().Add(-time.Second), nil
		}
		return validateSSOCodeToken(token)
	}}
	router := ssoFixtureRouter(handler, urlPrefixConfig{})
	token := signSSOCodeFixture(t, nil)
	verifier := strings.Repeat("a", 43)
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	bindings := ssoCodeBindings{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, State: state,
		Challenge: ssoDigest(verifier), ChallengeMethod: "S256"}
	issue := func() ssoTokenRequest {
		t.Helper()
		result := ssoFixtureRequest(router, "/sso/code", bindings, token, "https://sso.laisky.com")
		require.Equal(t, http.StatusOK, result.Code)
		require.Equal(t, "no-store", result.Header().Get("Cache-Control"))
		require.Equal(t, "no-referrer", result.Header().Get("Referrer-Policy"))
		require.NotContains(t, result.Body.String(), token)
		var body struct {
			Code    string `json:"code"`
			Expires int    `json:"expires_in"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, validSSOEntropy(body.Code))
		require.Positive(t, body.Expires)
		require.LessOrEqual(t, body.Expires, 60)
		require.Equal(t, 60*time.Second, store.TTL("sso:blog:code:"+ssoDigest(body.Code)))
		require.False(t, store.Exists("sso:blog:code:"+body.Code))
		return ssoTokenRequest{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, Code: body.Code, State: state, Verifier: verifier}
	}
	t.Run("bindings do not burn code", func(t *testing.T) {
		valid := issue()
		for _, change := range []func(*ssoTokenRequest){
			func(r *ssoTokenRequest) { r.State = strings.Repeat("A", 43) },
			func(r *ssoTokenRequest) { r.Verifier = strings.Repeat("b", 43) },
			func(r *ssoTokenRequest) { r.RedirectURI = blogSSOOrigin + "/" },
			func(r *ssoTokenRequest) { r.ClientID = "other" },
		} {
			invalid := valid
			change(&invalid)
			require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", invalid, "", blogSSOOrigin).Code)
		}
		result := ssoFixtureRequest(router, "/sso/token", valid, "", blogSSOOrigin)
		require.Equal(t, http.StatusOK, result.Code)
		var body struct {
			Token string `json:"access_token"`
			Type  string `json:"token_type"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
		require.True(t, body.Token == token)
		require.Equal(t, "Bearer", body.Type)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", valid, "", blogSSOOrigin).Code)
	})
	t.Run("expires in sixty seconds", func(t *testing.T) {
		request := issue()
		store.FastForward(60 * time.Second)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin).Code)
	})
	t.Run("session becomes inactive", func(t *testing.T) {
		request := issue()
		inactive.Store(true)
		defer inactive.Store(false)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin).Code)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin).Code)
	})
	t.Run("bearer expires after issuance", func(t *testing.T) {
		request := issue()
		expired.Store(true)
		defer expired.Store(false)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin).Code)
	})
	t.Run("inactive account cannot issue", func(t *testing.T) {
		inactive.Store(true)
		defer inactive.Store(false)
		require.Equal(t, http.StatusUnauthorized, ssoFixtureRequest(router, "/sso/code", bindings, token, "").Code)
	})
	t.Run("one concurrent winner", func(t *testing.T) {
		request := issue()
		var winners atomic.Int32
		var wait sync.WaitGroup
		for range 16 {
			wait.Go(func() {
				if ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin).Code == http.StatusOK {
					winners.Add(1)
				}
			})
		}
		wait.Wait()
		require.Equal(t, int32(1), winners.Load())
	})
	t.Run("invalid issuance and origin", func(t *testing.T) {
		for _, uri := range []string{blogSSOOrigin + "/", blogSSOOrigin + "/profile", blogSSOOrigin + ":443",
			"https://user@blog.laisky.com", "http://blog.laisky.com", blogSSOOrigin + "?x=1", blogSSOOrigin + "#x"} {
			invalid := bindings
			invalid.RedirectURI = uri
			require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/code", invalid, token, "").Code)
		}
		require.Equal(t, http.StatusUnauthorized, ssoFixtureRequest(router, "/sso/code", bindings, "", "").Code)
		require.Equal(t, http.StatusUnauthorized, ssoFixtureRequest(router, "/sso/code", bindings, "not-a-valid-sso-token", "").Code)
		require.Equal(t, http.StatusForbidden, ssoFixtureRequest(router, "/sso/code", bindings, token, "https://other.laisky.com").Code)
		request := issue()
		require.Equal(t, http.StatusForbidden, ssoFixtureRequest(router, "/sso/token", request, "", "https://other.laisky.com").Code)
		require.Equal(t, http.StatusBadRequest, ssoFixtureRequest(router, "/sso/token?code=forbidden", request, "", "").Code)
	})
}

// TestSSOCodeStrictClaims excludes legacy signatures and malformed/missing/expired SSO claims.
func TestSSOCodeStrictClaims(t *testing.T) {
	configureSSOCodeFixture(t)
	for name, change := range map[string]func(*jwt.UserClaims){
		"no expiry":    func(c *jwt.UserClaims) { c.ExpiresAt = nil },
		"expired":      func(c *jwt.UserClaims) { c.ExpiresAt = jwtLib.NewNumericDate(time.Now().Add(-time.Minute)) },
		"wrong issuer": func(c *jwt.UserClaims) { c.Issuer = "other" },
		"uid mismatch": func(c *jwt.UserClaims) { c.UID = "87654321-1234-4234-8234-123456789abc" },
		"not uuid":     func(c *jwt.UserClaims) { c.Subject = "legacy"; c.UID = "legacy" },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateSSOCodeToken(signSSOCodeFixture(t, change))
			require.Error(t, err)
		})
	}
	legacy, err := jwtLib.NewWithClaims(jwtLib.SigningMethodHS256, jwtLib.MapClaims{"sub": ssoTestUID}).SignedString([]byte("inert-legacy-key"))
	require.NoError(t, err)
	_, err = validateSSOCodeToken(legacy)
	require.Error(t, err)
	_, err = validateSSOCodeToken(signSSOCodeFixture(t, nil))
	require.NoError(t, err)
}

// TestSSOCodeRoutingAndBounds verifies root/proxy prefixes, strict preflight and bounded generic failures.
func TestSSOCodeRoutingAndBounds(t *testing.T) {
	for _, prefix := range []urlPrefixConfig{{}, {internal: "/mcp", public: "/mcp"}, {internal: "/mcp", public: ""}} {
		router := ssoFixtureRouter(ssoCodeHandler{}, prefix)
		for _, path := range ssoRoutePaths(prefix, "/sso/token") {
			getRequest := httptest.NewRequest(http.MethodGet, path, nil)
			getResult := httptest.NewRecorder()
			router.ServeHTTP(getResult, getRequest)
			require.Equal(t, http.StatusMethodNotAllowed, getResult.Code)
			request := httptest.NewRequest(http.MethodOptions, path, nil)
			request.Header.Set("Origin", blogSSOOrigin)
			result := httptest.NewRecorder()
			router.ServeHTTP(ssoDeadlineRecorder{result}, request)
			require.Equal(t, http.StatusNoContent, result.Code)
			require.Equal(t, blogSSOOrigin, result.Header().Get("Access-Control-Allow-Origin"))
			request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("x", ssoBodyLimit+1)))
			request.Header.Set("Content-Type", "application/json")
			result = httptest.NewRecorder()
			router.ServeHTTP(ssoDeadlineRecorder{result}, request)
			require.Equal(t, http.StatusBadRequest, result.Code)
			require.JSONEq(t, `{"error":"invalid_grant"}`, result.Body.String())
		}
	}
}

// TestSSOCodeMongoActiveAccount runs the production verifier against an ephemeral loopback-only Mongo fixture.
func TestSSOCodeMongoActiveAccount(t *testing.T) {
	parsed, err := url.Parse(os.Getenv("MONGO_QUERY_TEST_URI"))
	if os.Getenv("SSO_CODE_TEST_SANDBOX_OWNED") != "1" {
		t.Skip("explicitly owned disposable Mongo sandbox required")
	}
	if err != nil || parsed.Scheme != "mongodb" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Skip("loopback Mongo fixture is required for the active-account integration contract")
	}
	fixture := newSSOE2E(t)
	user := fixture.insertLegacyUser(t, "sso-code-local@example.test", "inert-local-password", true, bson.M{"status": "active"})
	token := signSSOCodeFixture(t, func(claims *jwt.UserClaims) { claims.UID = user.UID; claims.Subject = user.UID })
	store := miniredis.RunT(t)
	db := rlibs.NewDB(&redis.Options{Addr: store.Addr()})
	t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
	router := gin.New()
	prefix := urlPrefixConfig{}
	router.Use(ssoCodeTransport(prefix))
	registerSSOCodeRoutes(router, prefix, &Resolver{args: ResolverArgs{BlogSvc: fixture.svc, Rdb: db}})
	verifier := strings.Repeat("a", 43)
	state := strings.Repeat("A", 43)
	bindings := ssoCodeBindings{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, State: state,
		Challenge: ssoDigest(verifier), ChallengeMethod: "S256"}
	legacyClaims := jwt.NewUserClaims()
	legacyClaims.UID = user.UID
	legacyClaims.Subject = user.UID
	legacy, legacyErr := jwtLib.NewWithClaims(jwtLib.SigningMethodHS256, legacyClaims).SignedString([]byte(ssoE2ESecret))
	require.NoError(t, legacyErr)
	require.Equal(t, http.StatusUnauthorized, ssoFixtureRequest(router, "/sso/code", bindings, legacy, "").Code)
	response := ssoFixtureRequest(router, "/sso/code", bindings, token, "")
	require.Equal(t, http.StatusOK, response.Code)
	var issued struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &issued))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = fixture.db.Collection("users").UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{"status": "pending"}})
	require.NoError(t, err)
	request := ssoTokenRequest{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin, Code: issued.Code, State: state, Verifier: verifier}
	response = ssoFixtureRequest(router, "/sso/token", request, "", blogSSOOrigin)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, http.StatusUnauthorized, ssoFixtureRequest(router, "/sso/code", bindings, token, "").Code)
}

// TestSSOCodeStalledBody bounds an incomplete local HTTP POST without changing global server timeouts.
func TestSSOCodeStalledBody(t *testing.T) {
	router := ssoFixtureRouter(ssoCodeHandler{}, urlPrefixConfig{})
	server := httptest.NewServer(router)
	defer server.Close()
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	require.NoError(t, connection.SetDeadline(time.Now().Add(ssoRequestTimeout+2*time.Second)))
	started := time.Now()
	_, err = connection.Write([]byte("POST /sso/token HTTP/1.1\r\nHost: local.test\r\nContent-Type: application/json\r\nContent-Length: 400\r\n\r\n{"))
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Less(t, time.Since(started), ssoRequestTimeout+time.Second)
}

// TestSSOCodeCompletedBodyResetsDeadline preserves a keep-alive connection for subsequent legacy requests.
func TestSSOCodeCompletedBodyResetsDeadline(t *testing.T) {
	router := ssoFixtureRouter(ssoCodeHandler{}, urlPrefixConfig{})
	router.GET("/legacy", func(c *gin.Context) { c.String(http.StatusOK, "legacy unchanged") })
	server := httptest.NewServer(router)
	defer server.Close()
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	require.NoError(t, connection.SetDeadline(time.Now().Add(2*ssoRequestTimeout+2*time.Second)))
	bindings := ssoCodeBindings{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin,
		State: strings.Repeat("A", 43), Challenge: ssoDigest(strings.Repeat("a", 43)), ChallengeMethod: "S256"}
	body, err := json.Marshal(bindings)
	require.NoError(t, err)
	_, err = fmt.Fprintf(connection, "POST /sso/code HTTP/1.1\r\nHost: local.test\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	require.NoError(t, err)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	// Wait beyond the former SSO read deadline on the same open connection.
	time.Sleep(ssoRequestTimeout + 50*time.Millisecond)
	_, err = connection.Write([]byte("GET /legacy HTTP/1.1\r\nHost: local.test\r\n\r\n"))
	require.NoError(t, err)
	response, err = http.ReadResponse(reader, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	legacy, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "legacy unchanged", string(legacy))
}

// TestSSOCodeDuplicateInputs rejects duplicate JSON aliases and competing authorization headers.
func TestSSOCodeDuplicateInputs(t *testing.T) {
	router := ssoFixtureRouter(ssoCodeHandler{}, urlPrefixConfig{})
	bindings := ssoCodeBindings{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin,
		State: strings.Repeat("A", 43), Challenge: ssoDigest(strings.Repeat("a", 43)), ChallengeMethod: "S256"}
	base, err := json.Marshal(bindings)
	require.NoError(t, err)
	for _, key := range []string{"state", "STATE", `\u0073tate`} {
		body := string(base[:len(base)-1]) + `,"` + key + `":"` + bindings.State + `"}`
		request := httptest.NewRequest(http.MethodPost, "/sso/code", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer synthetic-fixture")
		result := httptest.NewRecorder()
		router.ServeHTTP(ssoDeadlineRecorder{result}, request)
		require.Equal(t, http.StatusBadRequest, result.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/sso/code", bytes.NewReader(base))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", "Bearer synthetic-first")
	request.Header.Add("Authorization", "Bearer synthetic-second")
	result := httptest.NewRecorder()
	router.ServeHTTP(ssoDeadlineRecorder{result}, request)
	require.Equal(t, http.StatusUnauthorized, result.Code)
	grant := ssoTokenRequest{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin,
		Code: strings.Repeat("A", 43), State: bindings.State, Verifier: strings.Repeat("a", 43)}
	raw, err := json.Marshal(grant)
	require.NoError(t, err)
	request = httptest.NewRequest(http.MethodPost, "/sso/token", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", "Bearer synthetic-first")
	request.Header.Add("Authorization", "Bearer synthetic-second")
	result = httptest.NewRecorder()
	router.ServeHTTP(ssoDeadlineRecorder{result}, request)
	require.Equal(t, http.StatusBadRequest, result.Code)
}

// TestSSOCodeActualCORSStack preserves scoped POST-only headers through the actual legacy CORS middleware.
func TestSSOCodeActualCORSStack(t *testing.T) {
	router := gin.New()
	router.Use(ssoCodeTransport(urlPrefixConfig{}), allowCORS)
	handler := ssoCodeHandler{}
	router.POST("/sso/token", handler.redeem)
	router.GET("/legacy", func(c *gin.Context) { c.String(http.StatusOK, "legacy") })
	grant := ssoTokenRequest{ClientID: blogSSOClient, RedirectURI: blogSSOOrigin,
		Code: strings.Repeat("A", 43), State: strings.Repeat("A", 43), Verifier: strings.Repeat("a", 43)}
	response := ssoFixtureRequest(router, "/sso/token", grant, "", blogSSOOrigin)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, blogSSOOrigin, response.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "POST", response.Header().Get("Access-Control-Allow-Methods"))
	require.Empty(t, response.Header().Get("Access-Control-Allow-Credentials"))
	require.Equal(t, []string{"Origin"}, response.Header().Values("Vary"))
	require.Equal(t, "Content-Type, Authorization", response.Header().Get("Access-Control-Allow-Headers"))
	denied := ssoFixtureRequest(router, "/sso/token", grant, "", "https://other.laisky.com")
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Empty(t, denied.Header().Get("Access-Control-Allow-Origin"))
	request := httptest.NewRequest(http.MethodGet, "/legacy", nil)
	request.Header.Set("Origin", "https://console.laisky.com")
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, request)
	require.Equal(t, http.StatusOK, legacy.Code)
	require.Equal(t, "true", legacy.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, legacy.Header().Get("Access-Control-Allow-Methods"), "GET")
}
