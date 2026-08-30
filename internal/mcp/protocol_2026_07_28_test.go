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

// TestServerSupportsMCPProtocol20260728 verifies discovery and tool listing for the latest protocol revision.
func TestServerSupportsMCPProtocol20260728(t *testing.T) {
	server := newProtocolTestServer(t)

	t.Run("server discover", func(t *testing.T) {
		response := performModernMCPRequest(t, server.Handler(), mcpgo.MethodServerDiscover, nil)
		require.Equal(t, http.StatusOK, response.Code)
		require.Empty(t, response.Header().Get(mcpgo.HeaderSessionID))

		payload := decodeMCPResponse(t, response.Body.Bytes())
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok, "expected result object in %s", response.Body.String())
		require.Equal(t, string(mcpgo.ResultTypeComplete), result["resultType"])

		versions, ok := result["supportedVersions"].([]any)
		require.True(t, ok, "expected supportedVersions in %s", response.Body.String())
		require.NotEmpty(t, versions)
		require.Equal(t, mcpgo.ProtocolVersion20260728, versions[0])

		meta, ok := result["_meta"].(map[string]any)
		require.True(t, ok, "expected result metadata in %s", response.Body.String())
		serverInfo, ok := meta[mcpgo.MetaKeyServerInfo].(map[string]any)
		require.True(t, ok, "expected server info in %s", response.Body.String())
		require.Equal(t, "LAISKY MCP SERVER", serverInfo["name"])
	})

	t.Run("tools list", func(t *testing.T) {
		response := performModernMCPRequest(t, server.Handler(), mcpgo.MethodToolsList, nil)
		require.Equal(t, http.StatusOK, response.Code)
		require.Empty(t, response.Header().Get(mcpgo.HeaderSessionID))

		payload := decodeMCPResponse(t, response.Body.Bytes())
		result, ok := payload["result"].(map[string]any)
		require.True(t, ok, "expected result object in %s", response.Body.String())
		require.Equal(t, string(mcpgo.ResultTypeComplete), result["resultType"])
		require.Contains(t, result, "ttlMs")
		require.Equal(t, string(mcpgo.CacheScopePrivate), result["cacheScope"])

		tools, ok := result["tools"].([]any)
		require.True(t, ok, "expected tools in %s", response.Body.String())
		require.True(t, toolListContains(tools, "mcp_pipe"), "expected mcp_pipe in %s", response.Body.String())
	})
}

// TestModernToolsListUsesPerRequestAuthorization verifies that stateless requests retain authorization-based tool filtering.
func TestModernToolsListUsesPerRequestAuthorization(t *testing.T) {
	const authorization = "Bearer sk-modern-tools-list"

	preferenceService := newUserPreferenceServiceForToolsListTest(t, "file:modern_tools_list_authorization?mode=memory&cache=shared")
	auth := mustAuthorizationContext(t, authorization)
	_, err := preferenceService.SetDisabledTools(context.Background(), auth, []string{"mcp_pipe"})
	require.NoError(t, err)

	server, err := NewServer(
		nil,
		nil,
		preferenceService,
		nil,
		rag.Settings{},
		nil,
		nil,
		nil,
		nil,
		ToolsSettings{MCPPipeEnabled: true},
		glog.Shared,
	)
	require.NoError(t, err)

	response := performModernMCPRequest(
		t,
		server.Handler(),
		mcpgo.MethodToolsList,
		nil,
		http.Header{"Authorization": []string{authorization}},
	)
	require.Equal(t, http.StatusOK, response.Code)
	require.Empty(t, response.Header().Get(mcpgo.HeaderSessionID))

	payload := decodeMCPResponse(t, response.Body.Bytes())
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok, "expected result object in %s", response.Body.String())
	tools, ok := result["tools"].([]any)
	require.True(t, ok, "expected tools in %s", response.Body.String())
	require.False(t, toolListContains(tools, "mcp_pipe"), "expected per-request authorization filtering in %s", response.Body.String())
}

// TestServerKeepsLegacyMCPInitializeCompatibility verifies that legacy clients can still negotiate through initialize.
func TestServerKeepsLegacyMCPInitializeCompatibility(t *testing.T) {
	server := newProtocolTestServer(t)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  mcpgo.MethodInitialize,
		"params": map[string]any{
			"protocolVersion": mcpgo.ProtocolVersion20251125,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "laisky-blog-graphql-test",
				"version": "1.0.0",
			},
		},
	})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	payload := decodeMCPResponse(t, response.Body.Bytes())
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok, "expected result object in %s", response.Body.String())
	require.Equal(t, mcpgo.ProtocolVersion20251125, result["protocolVersion"])
}

// newProtocolTestServer creates the smallest server that exposes a real tool catalog.
func newProtocolTestServer(t *testing.T) *Server {
	t.Helper()

	server, err := NewServer(
		nil,
		nil,
		nil,
		nil,
		rag.Settings{},
		nil,
		nil,
		nil,
		nil,
		ToolsSettings{MCPPipeEnabled: true},
		glog.Shared,
	)
	require.NoError(t, err)
	return server
}

// performModernMCPRequest sends a protocol 2026-07-28 request through an HTTP handler.
func performModernMCPRequest(t *testing.T, handler http.Handler, method mcpgo.MCPMethod, params map[string]any, additionalHeaders ...http.Header) *httptest.ResponseRecorder {
	t.Helper()

	requestParams := make(map[string]any, len(params)+1)
	for key, value := range params {
		requestParams[key] = value
	}
	requestParams["_meta"] = map[string]any{
		mcpgo.MetaKeyProtocolVersion: mcpgo.ProtocolVersion20260728,
		mcpgo.MetaKeyClientInfo: map[string]any{
			"name":    "laisky-blog-graphql-test",
			"version": "1.0.0",
		},
		mcpgo.MetaKeyClientCapabilities: map[string]any{},
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  requestParams,
	})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
	request.Header.Set(mcpgo.HeaderMethod, string(method))
	for _, headers := range additionalHeaders {
		for name, values := range headers {
			for _, value := range values {
				request.Header.Add(name, value)
			}
		}
	}

	rawParams, err := json.Marshal(requestParams)
	require.NoError(t, err)
	if name, ok := mcpgo.ExtractHeaderName(method, rawParams); ok {
		encodedName, encoded := mcpgo.EncodeHeaderValue(name)
		require.True(t, encoded)
		request.Header.Set(mcpgo.HeaderName, encodedName)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// decodeMCPResponse decodes one JSON-RPC response object for assertions.
func decodeMCPResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload), "response: %s", body)
	return payload
}

// toolListContains reports whether a decoded tools/list result contains the expected tool.
func toolListContains(tools []any, expectedName string) bool {
	for _, candidate := range tools {
		tool, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		if tool["name"] == expectedName {
			return true
		}
	}
	return false
}
