package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	glog "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

func TestNewServerRequiresCapability(t *testing.T) {
	srv, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{}, glog.Shared)
	require.Nil(t, srv)
	require.Error(t, err)
}

func TestHandleWebFetchReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	result, err := srv.handleWebFetch(context.Background(), mcpgo.CallToolRequest{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "web fetch is not configured", textContent.Text)
}

func TestHandleWebSearchReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	result, err := srv.handleWebSearch(context.Background(), mcpgo.CallToolRequest{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "web search is not configured", textContent.Text)
}

func TestHandleAskUserReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	req := mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Arguments: map[string]any{"question": "ping"}}}

	result, err := srv.handleAskUser(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "ask_user tool is not available", textContent.Text)
}

func TestHandleGetUserRequestReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	result, err := srv.handleGetUserRequest(context.Background(), mcpgo.CallToolRequest{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "get_user_request tool is not available", textContent.Text)
}

func TestHandleMemoryBeforeTurnReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	result, err := srv.handleMemoryBeforeTurn(context.Background(), mcpgo.CallToolRequest{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "memory_before_turn tool is not available", textContent.Text)
}

func TestHandleMemoryAfterTurnReturnsConfigurationError(t *testing.T) {
	srv := &Server{}

	result, err := srv.handleMemoryAfterTurn(context.Background(), mcpgo.CallToolRequest{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "memory_after_turn tool is not available", textContent.Text)
}

type mockRecorder struct {
	records []calllog.RecordInput
}

func (m *mockRecorder) Record(_ context.Context, input calllog.RecordInput) error {
	m.records = append(m.records, input)
	return nil
}

func TestHandleWebFetchRecordsCallLog(t *testing.T) {
	recorder := &mockRecorder{}
	srv := &Server{callLogger: recorder}
	req := mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Arguments: map[string]any{"url": "https://example.com"}}}

	result, err := srv.handleWebFetch(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, recorder.records, 1)

	record := recorder.records[0]
	require.Equal(t, "web_fetch", record.ToolName)
	require.Equal(t, calllog.StatusError, record.Status)
	require.Equal(t, 0, record.Cost)
	require.Contains(t, record.Parameters, "url")
}

func TestFilterToolsListBody(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"file_read"},{"name":"file_write"},{"name":"web_search"}]}}`)
	disabled := map[string]struct{}{"file_write": {}}

	filteredBody, changed, err := filterToolsListBody(body, disabled)
	require.NoError(t, err)
	require.True(t, changed)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(filteredBody, &payload))
	result, ok := payload["result"].(map[string]any)
	require.True(t, ok)
	toolsAny, ok := result["tools"].([]any)
	require.True(t, ok)
	require.Len(t, toolsAny, 2)

	names := make([]string, 0, len(toolsAny))
	for _, item := range toolsAny {
		tool, ok := item.(map[string]any)
		require.True(t, ok)
		name, ok := tool["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	require.ElementsMatch(t, []string{"file_read", "web_search"}, names)
}

func TestFilterToolsListBodyNoChange(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"file_read"}]}}`)
	disabled := map[string]struct{}{"web_search": {}}

	filteredBody, changed, err := filterToolsListBody(body, disabled)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, filteredBody)
}

func TestServerAvailableToolNames(t *testing.T) {
	srv := &Server{}
	require.Empty(t, srv.AvailableToolNames())
}

func TestLoadDisabledToolsForListRequestWithAuthorizationHeader(t *testing.T) {
	prefSvc := newUserPreferenceServiceForToolsListTest(t, "file:toolslist_header?mode=memory&cache=shared")
	auth := mustAuthorizationContext(t, "Bearer sk-tools-list-header")
	_, err := prefSvc.SetDisabledTools(context.Background(), auth, []string{"mcp_pipe"})
	require.NoError(t, err)

	request := newToolsListRequest("", "Bearer sk-tools-list-header")
	shouldFilter, disabled := loadDisabledToolsForListRequest(request, prefSvc, nil, nil)

	require.True(t, shouldFilter)
	_, ok := disabled["mcp_pipe"]
	require.True(t, ok)
}

func TestLoadDisabledToolsForListRequestWithSessionAuthorizationFallback(t *testing.T) {
	prefSvc := newUserPreferenceServiceForToolsListTest(t, "file:toolslist_session?mode=memory&cache=shared")
	auth := mustAuthorizationContext(t, "Bearer sk-tools-list-session")
	_, err := prefSvc.SetDisabledTools(context.Background(), auth, []string{"mcp_pipe"})
	require.NoError(t, err)

	sessionID := "mcp-session-tools-list"
	store := newSessionAuthorizationStore()
	cacheSessionAuthorizationForRequest(newGenericMCPRequest(sessionID, "Bearer sk-tools-list-session"), nil, store)

	request := newToolsListRequest(sessionID, "")
	shouldFilter, disabled := loadDisabledToolsForListRequest(request, prefSvc, nil, store)

	require.True(t, shouldFilter)
	_, ok := disabled["mcp_pipe"]
	require.True(t, ok)
}

func TestResolveAuthorizationForListRequestWithoutAuthorization(t *testing.T) {
	request := newToolsListRequest("", "")
	auth, source := resolveAuthorizationForListRequest(request, nil)
	require.Nil(t, auth)
	require.Equal(t, "none", source)
}

// newUserPreferenceServiceForToolsListTest builds a user preference service backed by sqlite for tools/list tests.
func newUserPreferenceServiceForToolsListTest(t *testing.T, dsn string) *userrequests.Service {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	service, err := userrequests.NewService(db, logSDK.Shared, nil, userrequests.Settings{})
	require.NoError(t, err)

	return service
}

// mustAuthorizationContext parses an Authorization header and returns a valid authorization context.
func mustAuthorizationContext(t *testing.T, authHeader string) *askuser.AuthorizationContext {
	t.Helper()

	auth, err := askuser.ParseAuthorizationContext(authHeader)
	require.NoError(t, err)
	return auth
}

// newGenericMCPRequest builds a generic MCP POST request with optional session and authorization headers.
func newGenericMCPRequest(sessionID string, authorization string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set(srv.HeaderKeySessionID, sessionID)
	}
	if strings.TrimSpace(authorization) != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

// newToolsListRequest builds a tools/list MCP request with optional session and authorization headers.
func newToolsListRequest(sessionID string, authorization string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"progressToken":2}}}`))
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set(srv.HeaderKeySessionID, sessionID)
	}
	if strings.TrimSpace(authorization) != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}
