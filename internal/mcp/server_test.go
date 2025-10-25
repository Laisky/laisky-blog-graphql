package mcp
package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

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

func TestHandleWebFetchRejectsEmptyURL(t *testing.T) {
	srv := &Server{
		rdb:    &rlibs.DB{},
		logger: log.Logger.Named("test"),
	}

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Arguments: map[string]any{
				"url": "   ",
			},
		},
	}

	result, err := srv.handleWebFetch(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "url cannot be empty", textContent.Text)
}

func TestHandleWebFetchRequiresAPIKey(t *testing.T) {
	srv := &Server{
		rdb:    &rlibs.DB{},
		logger: log.Logger.Named("test"),
	}

	req := mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Arguments: map[string]any{
				"url": "https://example.com",
			},
		},
	}

	result, err := srv.handleWebFetch(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	require.Equal(t, "missing authorization bearer token", textContent.Text)
}
