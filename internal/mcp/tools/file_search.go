package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileSearchTool implements the file_search MCP tool.
type FileSearchTool struct {
	svc FileService
}

// NewFileSearchTool constructs a FileSearchTool.
func NewFileSearchTool(svc FileService) (*FileSearchTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileSearchTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_search.
func (t *FileSearchTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_search",
		mcp.WithDescription("Search file content using hybrid retrieval."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query string.")),
		mcp.WithString("path_prefix", mcp.Description("Optional path prefix filter.")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of chunks to return.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes the file_search tool logic.
func (t *FileSearchTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	pathPrefix := readStringArg(req, "path_prefix")
	limit := readIntArg(req, "limit")
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.Search(ctx, auth, project, query, pathPrefix, limit)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}
		payload := map[string]any{"chunks": result.Chunks}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
