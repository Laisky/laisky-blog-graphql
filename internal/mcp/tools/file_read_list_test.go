package tools

import (
	"context"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

type readListStubService struct {
	lastReadLength int64
	lastListDepth  int
	lastListPath   string
	listErr        error
}

// Stat returns a stubbed stat response.
func (s *readListStubService) Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error) {
	return files.StatResult{}, nil
}

// Read records the requested length for assertions.
func (s *readListStubService) Read(_ context.Context, _ files.AuthContext, _ string, _ string, _ int64, length int64) (files.ReadResult, error) {
	s.lastReadLength = length
	return files.ReadResult{Content: "", ContentEncoding: "utf-8"}, nil
}

// Write returns a stubbed write response.
func (s *readListStubService) Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error) {
	return files.WriteResult{}, nil
}

// Delete returns a stubbed delete response.
func (s *readListStubService) Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error) {
	return files.DeleteResult{}, nil
}

// Rename returns a stubbed rename response.
func (s *readListStubService) Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error) {
	return files.RenameResult{}, nil
}

// List records the requested depth for assertions.
func (s *readListStubService) List(_ context.Context, _ files.AuthContext, _ string, path string, depth, _ int) (files.ListResult, error) {
	s.lastListDepth = depth
	s.lastListPath = path
	if s.listErr != nil {
		return files.ListResult{}, errors.WithStack(s.listErr)
	}
	return files.ListResult{}, nil
}

// Search returns a stubbed search response.
func (s *readListStubService) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return files.SearchResult{}, nil
}

// TestFileReadDefaultLength verifies missing length defaults to -1.
func TestFileReadDefaultLength(t *testing.T) {
	svc := &readListStubService{}
	tool, err := NewFileReadTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/a.txt",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	require.Equal(t, int64(-1), svc.lastReadLength)
}

// TestFileReadZeroLengthPreserved verifies explicit zero length is preserved.
func TestFileReadZeroLengthPreserved(t *testing.T) {
	svc := &readListStubService{}
	tool, err := NewFileReadTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/a.txt",
		"length":  0,
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	require.Equal(t, int64(0), svc.lastReadLength)
}

// TestFileListDefaultDepth verifies missing depth defaults to 1.
func TestFileListDefaultDepth(t *testing.T) {
	svc := &readListStubService{}
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	require.Equal(t, 1, svc.lastListDepth)
}

// TestFileListSlashPathNormalizedToRoot verifies "/" is normalized to root path.
func TestFileListSlashPathNormalizedToRoot(t *testing.T) {
	svc := &readListStubService{}
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	require.Equal(t, "", svc.lastListPath)
}

// TestFileListRootNotFoundReturnsEmpty verifies empty-root listings are normalized to empty responses.
func TestFileListRootNotFoundReturnsEmpty(t *testing.T) {
	svc := &readListStubService{listErr: files.NewError(files.ErrCodeNotFound, "path not found", false)}
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	payload := decodeToolPayload(t, result)
	require.Equal(t, false, payload["has_more"])
	entries, ok := payload["entries"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 0)
}

// TestFileListSlashRootNotFoundReturnsEmpty verifies slash-root NOT_FOUND is normalized to empty responses.
func TestFileListSlashRootNotFoundReturnsEmpty(t *testing.T) {
	svc := &readListStubService{listErr: files.NewError(files.ErrCodeNotFound, "path not found", false)}
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	payload := decodeToolPayload(t, result)
	require.Equal(t, false, payload["has_more"])
	entries, ok := payload["entries"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 0)
}

// TestFileListNonRootNotFoundReturnsError verifies non-root NOT_FOUND behavior remains unchanged.
func TestFileListNonRootNotFoundReturnsError(t *testing.T) {
	svc := &readListStubService{listErr: files.NewError(files.ErrCodeNotFound, "path not found", false)}
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project": "proj",
		"path":    "/missing",
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.True(t, result.IsError)
	payload := decodeToolPayload(t, result)
	require.Equal(t, string(files.ErrCodeNotFound), payload["code"])
}
