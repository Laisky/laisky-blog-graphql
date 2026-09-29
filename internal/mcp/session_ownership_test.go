package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

const reviewAlpha = "Bearer sk-review-session-alpha"
const reviewBeta = "Bearer sk-review-session-beta"

// TestMCPReviewLegacySessionOwnership exercises actual issued sessions, including non-tool methods.
func TestMCPReviewLegacySessionOwnership(t *testing.T) {
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			s := newReviewSessionServer(t)
			var calls atomic.Int64
			s.toolHandlers["test_session_effect"] = func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls.Add(1)
				return mcpgo.NewToolResultText("owner effect"), nil
			}
			owner := initializeReviewSession(t, s.Handler(), version, reviewAlpha)
			other := initializeReviewSession(t, s.Handler(), version, reviewBeta)
			require.NotEqual(t, owner.Get("Mcp-Session-Id"), other.Get("Mcp-Session-Id"))
			assertReviewCatalog(t, s.Handler(), owner, false)
			assertReviewCatalog(t, s.Handler(), other, true)

			for _, authorization := range []string{"", "Bearer", reviewBeta} {
				t.Run("reject_"+fmt.Sprint(len(authorization)), func(t *testing.T) {
					headers := owner.Clone()
					headers.Del("Authorization")
					if authorization != "" {
						headers.Set("Authorization", authorization)
					}
					for _, method := range []string{"tools/list", "tools/call", "notifications/initialized"} {
						t.Run(method, func(t *testing.T) {
							before := calls.Load()
							response := sendSDKWireRequest(t, s.Handler(), reviewSessionMessage(method), headers)
							require.Equal(t, before, calls.Load(), "rejected requests must have zero tool side effects")
							require.GreaterOrEqual(t, response.Code, 400, response.Body.String())
							require.Less(t, response.Code, 500)
							require.NotContains(t, response.Body.String(), "owner effect")
						})
					}
					for _, method := range []string{http.MethodGet, http.MethodDelete} {
						t.Run(method, func(t *testing.T) {
							// Bound even the unfixed server's streaming GET, so the red reproduction cannot hang.
							ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
							defer cancel()
							request := httptest.NewRequest(method, "/mcp/", nil).WithContext(ctx)
							request.Header = headers.Clone()
							request.Header.Set("Accept", "text/event-stream")
							response := httptest.NewRecorder()
							s.Handler().ServeHTTP(response, request)
							require.GreaterOrEqual(t, response.Code, 400, response.Body.String())
							require.Less(t, response.Code, 500)
						})
					}
				})
			}
			// An attacker cannot rebind or delete the owner's session.
			assertReviewCatalog(t, s.Handler(), owner, false)
			response := sendSDKWireRequest(t, s.Handler(), reviewSessionMessage("tools/call"), owner)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "owner effect")
		})
	}
}

// TestMCPReviewModernCatalogNeverInheritsSessionIdentity reproduces the legacy-cache bypass on modern requests.
func TestMCPReviewModernCatalogNeverInheritsSessionIdentity(t *testing.T) {
	s := newReviewSessionServer(t)
	owner := initializeReviewSession(t, s.Handler(), "2025-11-25", reviewAlpha)
	assertReviewCatalog(t, s.Handler(), owner, false)
	for _, authorization := range []string{"", "Bearer", reviewBeta, reviewAlpha, ""} {
		headers := http.Header{"Mcp-Session-Id": {owner.Get("Mcp-Session-Id")}}
		if authorization != "" {
			headers.Set("Authorization", authorization)
		}
		response := performModernMCPRequest(t, s.Handler(), mcpgo.MethodToolsList, nil, headers)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Empty(t, response.Header().Get("Mcp-Session-Id"))
		payload := decodeMCPResponse(t, response.Body.Bytes())
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok)
		catalog, ok := result["tools"].([]any)
		require.True(t, ok)
		require.Equal(t, authorization != reviewAlpha, toolListContains(catalog, "mcp_pipe"), "identity must come only from this request")
	}
}

// TestMCPReviewConcurrentSessionOwners verifies repeated parallel use without identity races.
func TestMCPReviewConcurrentSessionOwners(t *testing.T) {
	s := newReviewSessionServer(t)
	alpha := initializeReviewSession(t, s.Handler(), "2025-11-25", reviewAlpha)
	beta := initializeReviewSession(t, s.Handler(), "2025-11-25", reviewBeta)
	t.Run("concurrent", func(t *testing.T) {
		for i := range 24 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				headers := alpha.Clone()
				switch i % 3 {
				case 0:
					assertReviewCatalog(t, s.Handler(), headers, false)
				case 1:
					assertReviewCatalog(t, s.Handler(), beta.Clone(), true)
				default:
					headers.Set("Authorization", reviewBeta)
					response := sendSDKWireRequest(t, s.Handler(), reviewSessionMessage("tools/list"), headers)
					require.GreaterOrEqual(t, response.Code, 400)
					require.Less(t, response.Code, 500)
				}
			})
		}
	})
	assertReviewCatalog(t, s.Handler(), alpha, false)
}

// newReviewSessionServer provides real persisted preferences and the production HTTP handler.
func newReviewSessionServer(t *testing.T) *Server {
	t.Helper()
	preferences := newUserPreferenceServiceForToolsListTest(t, "file:"+t.Name()+"?mode=memory&cache=shared")
	_, err := preferences.SetDisabledTools(context.Background(), mustAuthorizationContext(t, reviewAlpha), []string{"mcp_pipe"})
	require.NoError(t, err)
	s, err := NewServer(nil, nil, preferences, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared)
	require.NoError(t, err)
	// Synthetic tool calls must not emit even zero-cost events to real billing.
	s.billingReporter = nil
	return s
}

// initializeReviewSession completes a legacy handshake and requires a real server-issued session ID.
func initializeReviewSession(t *testing.T, handler http.Handler, version, authorization string) http.Header {
	t.Helper()
	headers := http.Header{"Mcp-Protocol-Version": {version}}
	if authorization != "" {
		headers.Set("Authorization", authorization)
	}
	response := sendSDKWireRequest(t, handler, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": version, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "session-ownership-test", "version": "1"}},
	}, headers)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	session := response.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, session, "test must not silently fall back to sessionless coverage")
	headers.Set("Mcp-Session-Id", session)
	response = sendSDKWireRequest(t, handler, reviewSessionMessage("notifications/initialized"), headers)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	return headers
}

// assertReviewCatalog verifies a successful caller-specific tools/list response.
func assertReviewCatalog(t *testing.T, handler http.Handler, headers http.Header, pipeVisible bool) {
	t.Helper()
	response := sendSDKWireRequest(t, handler, reviewSessionMessage("tools/list"), headers)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	payload := decodeMCPResponse(t, response.Body.Bytes())
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok)
	catalog, ok := result["tools"].([]any)
	require.True(t, ok)
	require.Equal(t, pipeVisible, toolListContains(catalog, "mcp_pipe"))
}

// reviewSessionMessage creates requests using only a synthetic leaf tool.
func reviewSessionMessage(method string) map[string]any {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if method != "notifications/initialized" {
		message["id"] = 2
		message["params"] = map[string]any{}
	}
	if method == "tools/call" {
		message["params"] = map[string]any{"name": "mcp_pipe", "arguments": map[string]any{
			"spec": `{"steps":[{"id":"effect","tool":"test_session_effect"}]}`,
		}}
	}
	return message
}
