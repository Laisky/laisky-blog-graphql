package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

// TestMCPStableSDKToolCalls exercises the application handler, not only SDK discovery.
func TestMCPStableSDKToolCalls(t *testing.T) {
	s := newProtocolTestServer(t)
	// Substitute only the leaf dependency; transport, authorization, dispatch and
	// the real pipeline handler remain in the production request path.
	s.toolHandlers["test_echo"] = func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText(req.GetString("message", "")), nil
	}
	for _, tt := range []struct {
		name    string
		spec    string
		isError bool
		text    string
	}{
		{"success", `{"steps":[{"id":"one","tool":"test_echo","args":{"message":"upgrade-echo"}}]}`, false, "upgrade-echo"},
		{"validation error", `{"steps":[]}`, true, "steps cannot be empty"},
		{"leaf error", `{"steps":[{"id":"one","tool":"missing_tool"}]}`, true, "unknown tool"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := performModernMCPRequest(t, s.Handler(), mcpgo.MethodToolsCall, map[string]any{
				"name": "mcp_pipe", "arguments": map[string]any{"spec": tt.spec},
			})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Empty(t, response.Header().Get("Mcp-Session-Id"))
			payload := decodeMCPResponse(t, response.Body.Bytes())
			require.NotContains(t, payload, "error", "tool failures belong in result.isError")
			result, ok := payload["result"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "complete", result["resultType"])
			isError, _ := result["isError"].(bool)
			require.Equal(t, tt.isError, isError)
			require.NotEmpty(t, result["content"])
			require.Contains(t, response.Body.String(), tt.text)
		})
	}
}

// TestMCPStableSDKRejectsMisroutedRequests prevents rejected requests from executing tools.
func TestMCPStableSDKRejectsMisroutedRequests(t *testing.T) {
	s := newProtocolTestServer(t)
	calls := 0
	s.toolHandlers["test_echo"] = func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		calls++
		return mcpgo.NewToolResultText("executed"), nil
	}
	for _, tt := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing method", func(r *http.Request) { r.Header.Del("Mcp-Method") }},
		{"wrong method", func(r *http.Request) { r.Header.Set("Mcp-Method", "tools/list") }},
		{"missing tool name", func(r *http.Request) { r.Header.Del("Mcp-Name") }},
		{"wrong tool name", func(r *http.Request) { r.Header.Set("Mcp-Name", "another_tool") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tt.mutate(r)
				s.Handler().ServeHTTP(w, r)
			})
			response := performModernMCPRequest(t, mutated, mcpgo.MethodToolsCall, map[string]any{
				"name": "mcp_pipe", "arguments": map[string]any{
					"spec": `{"steps":[{"id":"one","tool":"test_echo"}]}`,
				},
			})
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			payload := decodeMCPResponse(t, response.Body.Bytes())
			protocolErr, ok := payload["error"].(map[string]any)
			require.True(t, ok)
			require.EqualValues(t, -32020, protocolErr["code"])
			require.Zero(t, calls, "header validation must happen before any tool side effect")
		})
	}
}

// TestMCPStableSDKRejectsUnknownVersion verifies the published error code independently of SDK constants.
func TestMCPStableSDKRejectsUnknownVersion(t *testing.T) {
	s := newProtocolTestServer(t)
	response := sendSDKWireRequest(t, s.Handler(), map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover",
		"params": map[string]any{"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2099-01-01",
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}},
	}, http.Header{"Mcp-Protocol-Version": {"2099-01-01"}, "Mcp-Method": {"server/discover"}})
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	payload := decodeMCPResponse(t, response.Body.Bytes())
	protocolErr, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, -32022, protocolErr["code"])
}

// TestMCPStableSDKCatalogIsolation checks alternating authenticated calls through one handler.
func TestMCPStableSDKCatalogIsolation(t *testing.T) {
	const alpha, beta = "Bearer sk-upgrade-alpha", "Bearer sk-upgrade-beta"
	preferences := newUserPreferenceServiceForToolsListTest(t, "file:sdk_upgrade_isolation?mode=memory&cache=shared")
	_, err := preferences.SetDisabledTools(context.Background(), mustAuthorizationContext(t, alpha), []string{"mcp_pipe"})
	require.NoError(t, err)
	s, err := NewServer(nil, nil, preferences, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, glog.Shared)
	require.NoError(t, err)
	for _, authorization := range []string{alpha, beta, alpha, beta} {
		response := performModernMCPRequest(t, s.Handler(), mcpgo.MethodToolsList, nil,
			http.Header{"Authorization": {authorization}})
		require.Equal(t, http.StatusOK, response.Code)
		require.Empty(t, response.Header().Get("Mcp-Session-Id"))
		payload := decodeMCPResponse(t, response.Body.Bytes())
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "complete", result["resultType"])
		require.Equal(t, "private", result["cacheScope"])
		require.Contains(t, result, "ttlMs")
		catalog, ok := result["tools"].([]any)
		require.True(t, ok)
		require.Equal(t, authorization == beta, toolListContains(catalog, "mcp_pipe"))
		require.NotContains(t, response.Body.String(), alpha)
		require.NotContains(t, response.Body.String(), beta)
	}
}

// TestMCPStableSDKLegacyRoundTrips protects old clients beyond the initialize response.
func TestMCPStableSDKLegacyRoundTrips(t *testing.T) {
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			s := newProtocolTestServer(t)
			response := sendSDKWireRequest(t, s.Handler(), map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "initialize",
				"params": map[string]any{"protocolVersion": version, "capabilities": map[string]any{},
					"clientInfo": map[string]any{"name": "sdk-upgrade-test", "version": "1"}},
			}, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			payload := decodeMCPResponse(t, response.Body.Bytes())
			result, ok := payload["result"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, version, result["protocolVersion"])
			headers := http.Header{"Mcp-Protocol-Version": {version}}
			if session := response.Header().Get("Mcp-Session-Id"); session != "" {
				headers.Set("Mcp-Session-Id", session)
			}
			response = sendSDKWireRequest(t, s.Handler(), map[string]any{
				"jsonrpc": "2.0", "method": "notifications/initialized",
			}, headers)
			require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
			response = sendSDKWireRequest(t, s.Handler(), map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
			}, headers)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			payload = decodeMCPResponse(t, response.Body.Bytes())
			result, ok = payload["result"].(map[string]any)
			require.True(t, ok)
			catalog, ok := result["tools"].([]any)
			require.True(t, ok)
			require.True(t, toolListContains(catalog, "mcp_pipe"))
		})
	}
}

// sendSDKWireRequest sends explicit wire messages so tests need no SDK client implementation.
func sendSDKWireRequest(t *testing.T, handler http.Handler, message map[string]any, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(message)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for name, values := range headers {
		request.Header[name] = values
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
