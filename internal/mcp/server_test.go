package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	glog "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

func TestNewServerRequiresCapability(t *testing.T) {
	srv, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, ToolsSettings{}, glog.Shared)
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
