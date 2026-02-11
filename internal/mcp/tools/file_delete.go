package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileDeleteTool implements the file_delete MCP tool.
type FileDeleteTool struct {
	svc FileService
}

// NewFileDeleteTool constructs a FileDeleteTool.
func NewFileDeleteTool(svc FileService) (*FileDeleteTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileDeleteTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_delete.
func (t *FileDeleteTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_delete",
		mcp.WithDescription("Delete a file or directory subtree."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Description("File or directory path; empty string means project root.")),
		mcp.WithBoolean("recursive", mcp.Description("Delete descendants when target is a directory.")),
		mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the file_delete tool logic.
func (t *FileDeleteTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path := readStringArg(req, "path")
	recursive := readBoolArg(req, "recursive")
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.Delete(ctx, auth, project, path, recursive)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}
		payload := map[string]any{"deleted_count": result.DeletedCount}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
