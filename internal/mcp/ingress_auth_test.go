package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

const issuedIngressKey = "sk-issued-ingress-key"

// oneAPITokenFixture emulates one-api's read-only GET /api/token/balance,
// which sits behind TokenAuth: 200 for an issued key, 401 for anything else.
type oneAPITokenFixture struct {
	*httptest.Server
	calls atomic.Int64
}

// newOneAPITokenFixture starts the fixture.
func newOneAPITokenFixture(t *testing.T, issued ...string) *oneAPITokenFixture {
	t.Helper()
	fixture := &oneAPITokenFixture{}
	allowed := make(map[string]bool, len(issued))
	for _, key := range issued {
		allowed[key] = true
	}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		if r.URL.Path != "/api/token/balance" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if !allowed[key] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"token not found","type":"one_api_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"remain_quota":1,"used_quota":0,"unlimited_quota":false}}`))
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

// authenticator returns the production one-api validator pointed at the fixture.
func (f *oneAPITokenFixture) authenticator(t *testing.T, options ...mcpauth.AuthenticatorOptions) *mcpauth.Authenticator {
	t.Helper()
	validator, err := mcpauth.NewOneAPITokenValidator(f.URL, nil)
	require.NoError(t, err)
	opts := mcpauth.AuthenticatorOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	opts.Name = "oneapi"
	authenticator, err := mcpauth.NewAuthenticator(validator, opts)
	require.NoError(t, err)
	return authenticator
}

// newIngressTestServer builds an MCP server, validating bearers against the
// fixture, with a side-effecting test tool.
func newIngressTestServer(t *testing.T, fixture *oneAPITokenFixture, options ...ServerOption) (*Server, *atomic.Int64) {
	t.Helper()
	options = append([]ServerOption{WithAuthenticator(fixture.authenticator(t))}, options...)
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared, options...)
	require.NoError(t, err)
	s.billingReporter = nil
	effects := &atomic.Int64{}
	s.toolHandlers["test_session_effect"] = func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		effects.Add(1)
		return mcpgo.NewToolResultText("tenant effect"), nil
	}
	return s, effects
}

// TestIngressRejectsUnissuedBearerBeforeSessionAndTools reproduces issue #86:
// a random bearer must be refused before a session exists or a tool runs.
func TestIngressRejectsUnissuedBearerBeforeSessionAndTools(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture)
	handler := s.Handler()
	random := "Bearer sk-random-" + strings.Repeat("x", 24)

	t.Run("legacy initialize", func(t *testing.T) {
		headers := http.Header{"Mcp-Protocol-Version": {mcpgo.ProtocolVersion20250618}, "Authorization": {random}}
		response := sendSDKWireRequest(t, handler, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": mcpgo.ProtocolVersion20250618, "capabilities": map[string]any{},
				"clientInfo": map[string]any{"name": "ingress-auth-test", "version": "1"}},
		}, headers)
		requireInvalidTokenChallenge(t, response)
		require.Empty(t, response.Header().Get(mcpgo.HeaderSessionID), "no session may be issued to an unvalidated bearer")
	})

	t.Run("modern stateless tools/call", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", random))
		requireInvalidTokenChallenge(t, response)
	})

	t.Run("query api key fallback", func(t *testing.T) {
		request := newModernToolCallRequest(t, context.Background(), "/mcp/?apikey=sk-random-query-key", "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		requireInvalidTokenChallenge(t, response)
	})

	require.Zero(t, effects.Load(), "an unvalidated bearer must never reach a tool handler")

	t.Run("issued key keeps working and is validated once", func(t *testing.T) {
		before := fixture.calls.Load()
		owner := initializeReviewSession(t, handler, mcpgo.ProtocolVersion20250618, "Bearer "+issuedIngressKey)
		response := sendSDKWireRequest(t, handler, reviewSessionMessage("tools/call"), owner)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "tenant effect")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "tenant effect")
		require.Equal(t, int64(2), effects.Load())
		require.Equal(t, before+1, fixture.calls.Load(), "a validated key must be cached across requests")
	})
}

// newModernToolCallRequest builds a stateless 2026-07-28 tools/call that runs
// the side-effecting test tool through mcp_pipe.
func newModernToolCallRequest(t *testing.T, ctx context.Context, target, authorization string) *http.Request {
	t.Helper()
	params := map[string]any{
		"name":      "mcp_pipe",
		"arguments": map[string]any{"spec": `{"steps":[{"id":"effect","tool":"test_session_effect"}]}`},
		"_meta": map[string]any{
			mcpgo.MetaKeyProtocolVersion:    mcpgo.ProtocolVersion20260728,
			mcpgo.MetaKeyClientInfo:         map[string]any{"name": "ingress-test", "version": "1"},
			mcpgo.MetaKeyClientCapabilities: map[string]any{},
		},
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": mcpgo.MethodToolsCall, "params": params})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	if strings.HasPrefix(target, "/") {
		request = httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)).WithContext(ctx)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
	request.Header.Set(mcpgo.HeaderMethod, string(mcpgo.MethodToolsCall))
	request.Header.Set(mcpgo.HeaderName, "mcp_pipe")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

// requireInvalidTokenChallenge asserts the RFC 6750 invalid_token rejection shape.
func requireInvalidTokenChallenge(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	challenge := response.Header().Get("WWW-Authenticate")
	require.True(t, strings.HasPrefix(challenge, "Bearer "), challenge)
	require.Contains(t, challenge, `error="invalid_token"`)
	require.Contains(t, challenge, `resource_metadata="`)
	require.NotContains(t, response.Body.String(), "tenant effect")
}

// TestIngressGateIsToolAgnostic sends a random bearer to every tool name, real
// or not: the gate runs before the SDK dispatches, so no handler is reachable.
func TestIngressGateIsToolAgnostic(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture)
	names := append(s.AvailableToolNames(), "file_write", "file_delete", "get_user_request", "memory_after_turn", "ask_user", "web_fetch")
	for i, name := range names {
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": map[string]any{}}})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("Mcp-Protocol-Version", mcpgo.ProtocolVersion20250618)
		request.Header.Set("Authorization", "Bearer sk-random-"+name)
		// Distinct callers, so the per-source budget does not turn rejections into 503s.
		request.RemoteAddr = fmt.Sprintf("192.0.2.%d:1000", i+1)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		requireInvalidTokenChallenge(t, response)
	}
	require.Zero(t, effects.Load())
}

// TestIngressFailsClosedWhenAuthorityUnavailable answers 503, never a session
// or a tool call, for a key one-api could not validate.
func TestIngressFailsClosedWhenAuthorityUnavailable(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture)
	fixture.Close()

	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey))
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Equal(t, "5", response.Header().Get("Retry-After"))
	require.Empty(t, response.Header().Get("WWW-Authenticate"), "an outage is not a credential rejection")
	require.Zero(t, effects.Load())
}

// TestIngressKeepsAnonymousDiscovery preserves initialize and tools/list
// without credentials, including a bearer scheme with an empty token.
func TestIngressKeepsAnonymousDiscovery(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, _ := newIngressTestServer(t, fixture)
	var tenants atomic.Int64
	s.toolHandlers["test_session_effect"] = func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		if _, ok := mcpauth.FromContext(ctx); ok {
			tenants.Add(1)
		}
		return mcpgo.NewToolResultText("anonymous step"), nil
	}
	for _, authorization := range []string{"", "Bearer", "Bearer   "} {
		headers := http.Header{"Mcp-Protocol-Version": {mcpgo.ProtocolVersion20250618}}
		if authorization != "" {
			headers.Set("Authorization", authorization)
		}
		response := sendSDKWireRequest(t, s.Handler(), map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": mcpgo.ProtocolVersion20250618, "capabilities": map[string]any{},
				"clientInfo": map[string]any{"name": "anonymous-discovery", "version": "1"}},
		}, headers)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		listing := httptest.NewRecorder()
		s.Handler().ServeHTTP(listing, newModernRequest(t, mcpgo.MethodToolsList, authorization))
		require.Equal(t, http.StatusOK, listing.Code, listing.Body.String())
		require.Contains(t, listing.Body.String(), "mcp_pipe")

		call := httptest.NewRecorder()
		s.Handler().ServeHTTP(call, newModernToolCallRequest(t, context.Background(), "/mcp/", authorization))
		require.Equal(t, http.StatusOK, call.Code, call.Body.String())
	}
	require.Zero(t, tenants.Load(), "an anonymous request never carries a tenant into a tool")
	require.Zero(t, fixture.calls.Load(), "anonymous discovery never consults the authority")

	oversized := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(strings.Repeat(" ", int(DefaultMCPAnonymousMaxBodyBytes)+1)))
	oversized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, oversized)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, "anonymous requests get the small discovery budget")
}

// TestIngressSSEStreamOutlivesReadBudget keeps a bodyless legacy SSE stream
// open well past the body read budget.
func TestIngressSSEStreamOutlivesReadBudget(t *testing.T) {
	const budget = 200 * time.Millisecond
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, _ := newIngressTestServer(t, fixture, WithRequestBodyBudget(RequestBodyBudget{ReadTimeout: budget}))
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)

	owner := initializeReviewSession(t, s.Handler(), mcpgo.ProtocolVersion20250618, "Bearer "+issuedIngressKey)
	ctx, cancel := context.WithTimeout(context.Background(), 4*budget)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/mcp/", nil)
	require.NoError(t, err)
	request.Header = owner.Clone()
	request.Header.Set("Accept", "text/event-stream")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, err = io.Copy(io.Discard, response.Body)
	require.ErrorIs(t, err, context.DeadlineExceeded, "only the client may end the stream, not the server's read budget")
}

// newModernRequest builds a stateless 2026-07-28 request without arguments.
func newModernRequest(t *testing.T, method mcpgo.MCPMethod, authorization string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": method, "params": map[string]any{
		"_meta": map[string]any{
			mcpgo.MetaKeyProtocolVersion:    mcpgo.ProtocolVersion20260728,
			mcpgo.MetaKeyClientInfo:         map[string]any{"name": "ingress-test", "version": "1"},
			mcpgo.MetaKeyClientCapabilities: map[string]any{},
		},
	}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
	request.Header.Set(mcpgo.HeaderMethod, string(method))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

// TestIngressSourceFloodCannotLockOutOtherCallers threads the peer address into
// the per-source budget: one address spraying random bearers exhausts only its
// own budget, and a different caller's never-seen key still validates.
func TestIngressSourceFloodCannotLockOutOtherCallers(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture, WithAuthenticator(fixture.authenticator(t, mcpauth.AuthenticatorOptions{
		UpstreamRatePerSecond: 0.001, UpstreamBurst: 10, PerSourceRatePerSecond: 0.001, PerSourceBurst: 3,
	})))
	unavailable := 0
	for i := range 40 {
		request := newModernToolCallRequest(t, context.Background(), "/mcp/", fmt.Sprintf("Bearer sk-spray-%02d", i))
		request.RemoteAddr = "203.0.113.50:5555"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code == http.StatusServiceUnavailable {
			unavailable++
			continue
		}
		requireInvalidTokenChallenge(t, response)
	}
	require.Equal(t, 37, unavailable, "beyond its own budget the source is refused without one-api calls")
	require.Equal(t, int64(3), fixture.calls.Load())

	request := newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey)
	request.RemoteAddr = "198.51.100.60:443"
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, int64(1), effects.Load())
}
