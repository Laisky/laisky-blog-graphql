package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestBalanceProxyBoundsTheKeyBeforeNormalizing reproduces a CPU exhaustion
// of the anonymous balance proxy: stripping "bearer " prefixes rescanned the
// whole remaining value per prefix, so one 1 MiB body of repeated prefixes
// cost about a minute of CPU (57 s measured). The key is now bounded before
// any work on it and normalized in linear time.
func TestBalanceProxyBoundsTheKeyBeforeNormalizing(t *testing.T) {
	var calls atomic.Int64
	oneapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer sk-proxy-valid" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":7}}`))
	}))
	t.Cleanup(oneapi.Close)
	validator, err := mcpauth.NewOneAPITokenValidator(oneapi.URL, nil)
	require.NoError(t, err)
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "oneapi"})
	require.NoError(t, err)
	authtest.Swap(t, authenticator)
	engine := newFollowupEngine(t)
	registerOneapiProxyRoutes(engine, urlPrefixConfig{public: "/p"})

	post := func(key string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"api_key": key})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/api/oneapi/balance", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "203.0.113.30:4444"
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(strings.Repeat("bearer ", 140_000)) }()
	select {
	case recorder := <-done:
		require.Equal(t, http.StatusOK, recorder.Code)
		require.JSONEq(t, `{"success":false,"message":"failed to fetch balance"}`, recorder.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("a body of repeated bearer prefixes must be refused without quadratic work")
	}

	recorder := post(" sk-" + strings.Repeat("k", mcpauth.MaxAPIKeyLength) + " ")
	require.JSONEq(t, `{"success":false,"message":"failed to fetch balance"}`, recorder.Body.String())
	require.Zero(t, calls.Load(), "an oversized key never reaches the authority")

	require.Equal(t, "sk-x", normalizeOneapiAPIKey("  Bearer  BEARER sk-x "), "repeated prefixes still normalize")
	recorder = post("Bearer sk-proxy-valid")
	require.Contains(t, recorder.Body.String(), `"success":true`)
	require.Equal(t, int64(1), calls.Load())
}
