package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileListTool implements the file_list MCP tool.
type FileListTool struct {
	svc FileService
}

// NewFileListTool constructs a FileListTool.
func NewFileListTool(svc FileService) (*FileListTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileListTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_list.
func (t *FileListTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_list",
		mcp.WithDescription("List files and directories under a path."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Description("Directory path; empty string means project root.")),
		mcp.WithNumber("depth", mcp.Description("Depth of traversal; 0 lists the path itself.")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of entries to return.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes the file_list tool logic.
func (t *FileListTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path := readStringArg(req, "path")
	depth := readIntArgWithDefault(req, "depth", 1)
	limit := readIntArg(req, "limit")
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.List(ctx, auth, project, path, depth, limit)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}
		payload := map[string]any{"entries": result.Entries, "has_more": result.HasMore}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
