package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	glog "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
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

	// The bearer gate runs first and stores the verified caller on the request.
	request := newToolsListRequest("", "Bearer sk-tools-list-header")
	request = request.WithContext(authtest.WithVerified(request.Context(), "Bearer sk-tools-list-header"))
	shouldFilter, disabled := loadDisabledToolsForListRequest(request, prefSvc, nil)

	require.True(t, shouldFilter)
	_, ok := disabled["mcp_pipe"]
	require.True(t, ok)
}

// TestLoadDisabledToolsForListRequestDoesNotUseSessionIdentity protects anonymous catalogs.
func TestLoadDisabledToolsForListRequestDoesNotUseSessionIdentity(t *testing.T) {
	prefSvc := newUserPreferenceServiceForToolsListTest(t, "file:toolslist_session?mode=memory&cache=shared")
	auth := mustAuthorizationContext(t, "Bearer sk-tools-list-session")
	_, err := prefSvc.SetDisabledTools(context.Background(), auth, []string{"mcp_pipe"})
	require.NoError(t, err)

	request := newToolsListRequest("mcp-session-tools-list", "")
	shouldFilter, disabled := loadDisabledToolsForListRequest(request, prefSvc, nil)
	require.False(t, shouldFilter)
	require.Empty(t, disabled, "a session must not supply the previous caller's identity")
}

// TestLoadDisabledToolsForListRequestIgnoresUnverifiedHeader proves the filter
// never derives a tenant from a raw header the gate did not verify, and leaves
// such a body unread.
func TestLoadDisabledToolsForListRequestIgnoresUnverifiedHeader(t *testing.T) {
	prefSvc := newUserPreferenceServiceForToolsListTest(t, "file:toolslist_unverified?mode=memory&cache=shared")
	const payload = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	for _, target := range []string{"/mcp/", "/mcp/?APIKEY=sk-tools-list-query"} {
		reader := strings.NewReader(payload)
		request := httptest.NewRequest(http.MethodPost, target, reader)
		request.Header.Set("Authorization", "Bearer sk-tools-list-unverified")
		shouldFilter, disabled := loadDisabledToolsForListRequest(request, prefSvc, nil)
		require.False(t, shouldFilter)
		require.Empty(t, disabled)
		require.Equal(t, len(payload), reader.Len(), "an unverified request body must not be read")
	}
}

// TestLoadDisabledToolsForListRequestBoundsPeek keeps an oversized body intact
// for the SDK and skips filtering instead of buffering it.
func TestLoadDisabledToolsForListRequestBoundsPeek(t *testing.T) {
	prefSvc := newUserPreferenceServiceForToolsListTest(t, "file:toolslist_peek?mode=memory&cache=shared")
	payload := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", toolsListPeekLimit) + `"}}`
	request := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(payload))
	request = request.WithContext(authtest.WithVerified(request.Context(), "Bearer sk-tools-list-peek"))
	shouldFilter, _ := loadDisabledToolsForListRequest(request, prefSvc, nil)
	require.False(t, shouldFilter)
	replayed, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, payload, string(replayed), "the SDK must still receive the complete body")
}

func TestResolveRequestAuthorizationHeader(t *testing.T) {
	t.Run("header first", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp/?APIKEY=sk-query-token", nil)
		req.Header.Set("Authorization", "Bearer sk-header-token")

		authHeader, source := resolveRequestAuthorizationHeader(req)
		require.Equal(t, "Bearer sk-header-token", authHeader)
		require.Equal(t, "header", source)
	})

	t.Run("query fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp/?api_key=Bearer%20sk-query-token", nil)

		authHeader, source := resolveRequestAuthorizationHeader(req)
		require.Equal(t, "Bearer sk-query-token", authHeader)
		require.Equal(t, "query_apikey", source)
	})

	t.Run("none", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp/", nil)

		authHeader, source := resolveRequestAuthorizationHeader(req)
		require.Equal(t, "", authHeader)
		require.Equal(t, "none", source)
	})
}

func TestWithAuthorizationHeaderNormalization(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp/?APIKEY=sk-normalize-query", nil)
	recorder := httptest.NewRecorder()

	h := withAuthorizationHeaderNormalization(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer sk-normalize-query", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}), nil)

	h.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

// newUserPreferenceServiceForToolsListTest builds a user preference service backed by sqlite for tools/list tests.
func newUserPreferenceServiceForToolsListTest(t *testing.T, dsn string) *userrequests.Service {
	t.Helper()

	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	service, err := userrequests.NewService(db, logSDK.Shared, nil, userrequests.Settings{})
	require.NoError(t, err)

	return service
}

// mustAuthorizationContext parses an Authorization header and returns a valid authorization context.
func mustAuthorizationContext(t *testing.T, authHeader string) *askuser.AuthorizationContext {
	t.Helper()

	return askuser.FromSharedContext(authtest.Verified(authHeader))
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
