package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	glog "github.com/Laisky/go-utils/v6/log"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
)

func TestNewServerRequiresCapability(t *testing.T) {
	srv, err := NewServer(nil, nil, nil, rag.Settings{}, nil, nil, glog.Shared)
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
