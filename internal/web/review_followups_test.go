package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// newFollowupEngine builds the production engine (client-identity policy with
// its defaults, body budgets and shared middleware).
func newFollowupEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	policy, err := clientip.LoadPolicy(func(string) any { return nil })
	require.NoError(t, err)
	engine, err := newServerEngine(policy, urlPrefixConfig{})
	require.NoError(t, err)
	return engine
}

// TestRV95SingleHostXFFRotationLocksOutNewValidKeys: through the production
// engine, one host rotating X-Forwarded-For stays one source, so its spray of
// random bearers cannot drain the global budget and lock out another client's
// never-seen valid key. Red on 1cc33db (attacker spent 30 upstream calls, victim 503).
func TestRV95SingleHostXFFRotationLocksOutNewValidKeys(t *testing.T) {
	var calls atomic.Int64
	validator := mcpauth.ValidatorFunc(func(_ context.Context, key string) error {
		calls.Add(1)
		if key == "sk-victim-new" {
			return nil
		}
		return mcpauth.ErrCredentialRejected
	})
	frozen := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "oneapi", Now: func() time.Time { return frozen }})
	require.NoError(t, err)

	engine := newFollowupEngine(t)
	console := mcpauth.HTTPMiddlewareWith(authenticator, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	engine.Any("/tools/get_user_requests/api/*path", gin.WrapH(console))

	send := func(remote, xff, key string) int {
		request := httptest.NewRequest(http.MethodGet, "/tools/get_user_requests/api/requests", nil)
		request.RemoteAddr = remote
		if xff != "" {
			request.Header.Set("X-Forwarded-For", xff)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder.Code
	}

	for i := range 40 {
		// A direct (untrusted) peer rotating a spoofed leftmost address.
		send("203.0.113.9:4444", fmt.Sprintf("10.%d.%d.%d, 203.0.113.9", i/65536, (i/256)%256, i%256), fmt.Sprintf("sk-random-%d", i))
		// The same host behind the trusted nginx peer: only the address nginx appended counts.
		send("100.64.0.10:443", fmt.Sprintf("198.18.%d.%d, 203.0.113.10", i/256, i%256), fmt.Sprintf("sk-proxied-%d", i))
	}
	require.LessOrEqual(t, calls.Load(), int64(2*mcpauth.DefaultValidationSettings().PerSourceBurst),
		"each host spends only its own source budget")

	require.Equal(t, http.StatusOK, send("198.51.100.1:5555", "", "sk-victim-new"),
		"one host must not be able to lock out another client's valid key")
}

// TestRV95BalanceProxyFloodExhaustsOneAPIBudgetAndLocksOutValidation: the
// anonymous balance proxy goes through the same one-api gate, so flooding it
// spends only the flooder's source budget; one-api never reaches its per-IP
// limit and a never-seen valid key still validates. Red on 1cc33db (482
// one-api calls, victim 503).
func TestRV95BalanceProxyFloodExhaustsOneAPIBudgetAndLocksOutValidation(t *testing.T) {
	var served atomic.Int64
	oneapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) > 480 { // one-api GlobalAPIRateLimit for this server's egress IP
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("Authorization") == "Bearer sk-victim-valid" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":1}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(oneapi.Close)

	validator, err := mcpauth.NewOneAPITokenValidator(oneapi.URL, nil)
	require.NoError(t, err)
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "oneapi"})
	require.NoError(t, err)
	authtest.Swap(t, authenticator)

	engine := newFollowupEngine(t)
	registerOneapiProxyRoutes(engine, urlPrefixConfig{public: "/p"})
	engine.Any("/tools/get_user_requests/api/*path", gin.WrapH(mcpauth.HTTPMiddlewareWith(authenticator,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))))

	for i := range 481 {
		request := httptest.NewRequest(http.MethodPost, "/api/oneapi/balance",
			strings.NewReader(fmt.Sprintf(`{"api_key":"sk-junk-%d"}`, i)))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "203.0.113.9:4444"
		engine.ServeHTTP(httptest.NewRecorder(), request)
	}
	require.LessOrEqual(t, served.Load(), int64(mcpauth.DefaultValidationSettings().PerSourceBurst))

	request := httptest.NewRequest(http.MethodGet, "/tools/get_user_requests/api/requests", nil)
	request.RemoteAddr = "198.51.100.1:5555"
	request.Header.Set("Authorization", "Bearer sk-victim-valid")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "an anonymous flood of a balance proxy must not lock out valid keys")

	// The proxy still returns real numbers for a valid key.
	request = httptest.NewRequest(http.MethodPost, "/api/oneapi/balance", strings.NewReader(`{"api_key":"sk-victim-valid"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.1:5555"
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.JSONEq(t, `{"success":true,"message":"","data":{"remain_quota":1,"used_quota":0}}`, recorder.Body.String())
}

// TestProductionEngineAdmitsSlowSteadyLargeUploads sends a body far slower than
// a flat 60s/64 MiB budget would require but above the minimum data rate, and
// requires it to complete, while a stalled body on the same route is cut.
func TestProductionEngineAdmitsSlowSteadyLargeUploads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{
			"settings.web.request_body.read_grace":    "300ms",
			"settings.web.request_body.min_data_rate": 4096,
		}[key]
	})
	outcomes := make(chan string, 2)
	engine := gin.New()
	engine.Use(newRequestBodyPolicy(urlPrefixConfig{}, settings).middleware())
	engine.POST("/query", func(c *gin.Context) {
		n, err := io.Copy(io.Discard, c.Request.Body)
		if err != nil {
			outcomes <- "cut"
			return
		}
		outcomes <- fmt.Sprintf("read %d", n)
		c.Status(http.StatusNoContent)
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	// 32 KiB at about 16 KiB/s: 2 s, well past the 300ms grace.
	const total = 32 << 10
	pr, pw := io.Pipe()
	go func() {
		chunk := make([]byte, 2<<10)
		for sent := 0; sent < total; sent += len(chunk) {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
			time.Sleep(125 * time.Millisecond)
		}
		_ = pw.Close()
	}()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/query", pr)
	require.NoError(t, err)
	request.ContentLength = total
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, fmt.Sprintf("read %d", total), <-outcomes, "a slow but live upload must complete")

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /query HTTP/1.1\r\nHost: test\r\nContent-Length: 4096\r\n\r\n{\"q\":")
	require.NoError(t, err)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "cut", outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled body was not cut")
	}
}
