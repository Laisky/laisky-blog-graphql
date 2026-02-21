package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileRenameTool implements the file_rename MCP tool.
type FileRenameTool struct {
	svc FileService
}

// NewFileRenameTool constructs a FileRenameTool.
func NewFileRenameTool(svc FileService) (*FileRenameTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileRenameTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_rename.
func (t *FileRenameTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_rename",
		mcp.WithDescription("Rename or move a file or directory path."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("from_path", mcp.Required(), mcp.Description("Source file or directory path.")),
		mcp.WithString("to_path", mcp.Required(), mcp.Description("Destination file or directory path.")),
		mcp.WithBoolean("overwrite", mcp.Description("When true, replace an existing destination file for file moves.")),
		mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the file_rename tool logic.
func (t *FileRenameTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fromPath, err := req.RequireString("from_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	toPath, err := req.RequireString("to_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	overwrite := readBoolArg(req, "overwrite")

	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.Rename(ctx, auth, project, fromPath, toPath, overwrite)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}

		payload := map[string]any{"moved_count": result.MovedCount}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}

	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
