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

type stubFileService struct {
	writeErr  error
	renameErr error
}

// Stat returns a stubbed stat result for tests.
func (s stubFileService) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	return files.StatResult{}, nil
}

// Read returns a stubbed read result for tests.
func (s stubFileService) Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error) {
	return files.ReadResult{}, nil
}

// Write returns the configured error for tests.
func (s stubFileService) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, s.writeErr
}

// Delete returns a stubbed delete result for tests.
func (s stubFileService) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

// Rename returns the configured error for tests.
func (s stubFileService) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, s.renameErr
}

// List returns a stubbed list result for tests.
func (s stubFileService) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return files.ListResult{}, nil
}

// Search returns a stubbed search result for tests.
func (s stubFileService) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// TestFileWriteMissingAuth verifies authorization checks.
func TestFileWriteMissingAuth(t *testing.T) {
	tool, err := NewFileWriteTool(stubFileService{})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/a.txt",
		"content": "data",
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.True(t, result.IsError)
}

// TestFileWriteErrorMapping verifies error payload formatting.
func TestFileWriteErrorMapping(t *testing.T) {
	serviceErr := files.NewError(files.ErrCodeInvalidPath, "bad path", false)
	tool, err := NewFileWriteTool(stubFileService{writeErr: serviceErr})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "key",
		APIKeyHash:   "hash",
		UserIdentity: "user:test",
	})

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/a.txt",
		"content": "data",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.True(t, result.IsError)

	if result.StructuredContent != nil {
		payload := result.StructuredContent.(map[string]any)
		require.Equal(t, string(files.ErrCodeInvalidPath), payload["code"])
		return
	}

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, string(files.ErrCodeInvalidPath), payload["code"])
}
