package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileRenameMissingAuth verifies authorization checks.
func TestFileRenameMissingAuth(t *testing.T) {
	tool, err := NewFileRenameTool(stubFileService{})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project":   "proj",
		"from_path": "/a.txt",
		"to_path":   "/b.txt",
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.True(t, result.IsError)
}

// TestFileRenameErrorMapping verifies error payload formatting.
func TestFileRenameErrorMapping(t *testing.T) {
	serviceErr := files.NewError(files.ErrCodeAlreadyExists, "destination path already exists", false)
	tool, err := NewFileRenameTool(stubFileService{renameErr: serviceErr})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "key",
		APIKeyHash:   "hash",
		UserIdentity: "user:test",
	})

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project":   "proj",
		"from_path": "/a.txt",
		"to_path":   "/b.txt",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.True(t, result.IsError)

	if result.StructuredContent != nil {
		payload := result.StructuredContent.(map[string]any)
		require.Equal(t, string(files.ErrCodeAlreadyExists), payload["code"])
		return
	}

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, string(files.ErrCodeAlreadyExists), payload["code"])
}
