package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

// ingressClock is a manually advanced clock for authenticator budgets and TTLs.
type ingressClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the current fake time.
func (c *ingressClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake time forward.
func (c *ingressClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// withFixtureConsume routes the server's production consume call
// (oneapi.ConsumeAt) to fixture, without touching the oneapi.BillingAPI global.
func withFixtureConsume(fixture *oneAPITokenFixture) ServerOption {
	return func(services *sharedServerServices) {
		services.consume = consumeAt(fixture.URL)
		services.consumeBaseURL = fixture.URL
	}
}

// TestIngressActiveKeyStaysServedThroughItsOwnConsumes reproduces review
// finding C of PR #95: every active key had to revalidate once per
// positive_ttl through the gate's budgets, so once a flood (or simply more
// active keys than the budgets cover) spent them, a key that kept working at
// one-api got 503s. Every tool call already sends a consume through one-api's
// TokenAuth, so an accepted consume is a fresh authority verdict: the key's
// trust period restarts from it and no extra validation call is needed. The
// revocation bound stays positive_ttl after the last acceptance.
func TestIngressActiveKeyStaysServedThroughItsOwnConsumes(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	clock := &ingressClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	// Budgets that admit exactly one validation and then stay spent, as
	// under a sustained flood.
	authenticator := fixture.authenticator(t, mcpauth.AuthenticatorOptions{
		Now: clock.Now, UpstreamRatePerSecond: 1e-6, UpstreamBurst: 1,
		PerSourceRatePerSecond: 1e-6, PerSourceBurst: 1, RevalidationRatePerSecond: -1,
	})
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared,
		WithAuthenticator(authenticator), withFixtureConsume(fixture))
	require.NoError(t, err)
	s.toolHandlers["test_session_effect"] = func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("tenant effect"), nil
	}
	call := func() int {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey))
		return response.Code
	}

	require.Equal(t, http.StatusOK, call())
	require.Eventually(t, func() bool { return fixture.consumes.Load() == 1 }, 5*time.Second, 5*time.Millisecond)

	clock.Advance(4 * time.Minute)
	require.Equal(t, http.StatusOK, call(), "within positive_ttl of the validation")
	require.Eventually(t, func() bool { return fixture.consumes.Load() == 2 }, 5*time.Second, 5*time.Millisecond)

	// 6 minutes after the only validation the budgets are still spent, but
	// one-api accepted the key's consume 2 minutes ago.
	clock.Advance(2 * time.Minute)
	require.Eventually(t, func() bool { return call() == http.StatusOK }, 2*time.Second, 10*time.Millisecond,
		"an active key one-api keeps accepting must not depend on the spent validation budget")
	require.Equal(t, int64(1), fixture.calls.Load(), "no extra validation call for an active key")
	require.Eventually(t, func() bool { return fixture.consumes.Load() == 3 }, 5*time.Second, 5*time.Millisecond)

	// Revocation stays bounded: without further accepted consumes the key
	// needs a validation again positive_ttl after its last acceptance.
	clock.Advance(5 * time.Minute)
	require.Equal(t, http.StatusServiceUnavailable, call())
}

// TestIngressConsumeRefreshNeedsTheValidatorsProof reproduces the
// independent review of this follow-up: any 200 consume answer refreshed a
// verdict, although validation treats a 200 HTML page or a non-success
// envelope as "no verdict", so a revoked key stayed verified while a proxy or
// maintenance page answered its consumes. Only one-api's 200 JSON envelope
// with "success": true counts; the positive control shows it does. The
// validation budgets are spent after the first call, so a key the consume did
// not refresh gets 503 (no verdict is recorded either way).
func TestIngressConsumeRefreshNeedsTheValidatorsProof(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType, body string
		refreshed         bool
	}{
		"html page":        {"text/html", "<html>maintenance</html>", false},
		"non-success JSON": {"application/json", `{"success":false,"message":"busy"}`, false},
		"success envelope": {"application/json", `{"success":true,"message":"","data":null}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			var balanceCalls, consumes atomic.Int64
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/token/consume" {
					consumes.Add(1)
					w.Header().Set("Content-Type", tc.contentType)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				balanceCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":1}}`))
			}))
			t.Cleanup(authority.Close)
			fixture := &oneAPITokenFixture{Server: authority}
			clock := &ingressClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
			authenticator := fixture.authenticator(t, mcpauth.AuthenticatorOptions{
				Now: clock.Now, UpstreamRatePerSecond: 1e-6, UpstreamBurst: 1,
				PerSourceRatePerSecond: 1e-6, PerSourceBurst: 1, RevalidationRatePerSecond: -1,
			})
			s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared,
				WithAuthenticator(authenticator), withFixtureConsume(fixture))
			require.NoError(t, err)
			s.toolHandlers["test_session_effect"] = func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText("tenant effect"), nil
			}
			call := func() int {
				response := httptest.NewRecorder()
				s.Handler().ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey))
				return response.Code
			}

			require.Equal(t, http.StatusOK, call())
			clock.Advance(4 * time.Minute)
			require.Equal(t, http.StatusOK, call(), "still within positive_ttl of the validation")
			require.Eventually(t, func() bool { return consumes.Load() == 2 }, 5*time.Second, 5*time.Millisecond)

			clock.Advance(2 * time.Minute)
			served := func() bool { return call() == http.StatusOK }
			if tc.refreshed {
				require.Eventually(t, served, 2*time.Second, 10*time.Millisecond, "an accepted consume restarts the trust period")
			} else {
				require.Never(t, served, 300*time.Millisecond, 10*time.Millisecond, "a consume answer that proves nothing refreshes nothing")
			}
			require.Equal(t, int64(1), balanceCalls.Load())
		})
	}
}
