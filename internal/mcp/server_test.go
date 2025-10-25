package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestNewServerRequiresCapability(t *testing.T) {
	srv, err := NewServer(nil, nil, nil, nil)
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
