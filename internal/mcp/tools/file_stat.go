package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileStatTool implements the file_stat MCP tool.
type FileStatTool struct {
	svc FileService
}

// NewFileStatTool constructs a FileStatTool.
func NewFileStatTool(svc FileService) (*FileStatTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileStatTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_stat.
func (t *FileStatTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_stat",
		mcp.WithDescription("Return metadata for a file or directory path."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Description("File path; empty string means project root.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes the file_stat tool logic.
func (t *FileStatTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path := readStringArg(req, "path")
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.Stat(ctx, auth, project, path)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}
		payload := map[string]any{
			"exists":     result.Exists,
			"type":       result.Type,
			"size":       result.Size,
			"created_at": result.CreatedAt,
			"updated_at": result.UpdatedAt,
		}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
