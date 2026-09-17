package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileRenameTool implements the file_rename MCP tool.
type FileRenameTool struct{ svc FileService }

// NewFileRenameTool constructs a FileRenameTool.
func NewFileRenameTool(svc FileService) (*FileRenameTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileRenameTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_rename.
func (t *FileRenameTool) Definition() mcp.Tool {
	return mcp.NewTool("file_rename",
		mcp.WithDescription("Rename one file using its required source expected_version. Non-overwriting moves require an absent destination. Overwriting moves also require "+
			"expected_destination_version or destination_must_not_exist=true. Directory moves are not supported by file tokens."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("from_path", mcp.Required(), mcp.Description("Source file path.")),
		mcp.WithString("to_path", mcp.Required(), mcp.Description("Destination file path.")),
		mcp.WithBoolean("overwrite", mcp.Description("When true, a destination version or destination_must_not_exist=true is also required.")),
		expectedFileVersionOption(true),
		mcp.WithString("expected_destination_version", mcp.Description("Version of the existing destination; requires overwrite=true.")),
		mcp.WithBoolean("destination_must_not_exist", mcp.Description("Require an absent destination. Implicit for non-overwriting moves.")),
		fileToolPluginOption(), mcp.WithIdempotentHintAnnotation(false),
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
	fromPath, toPath = normalizeFilePath(fromPath), normalizeFilePath(toPath)
	overwrite := readBoolArg(req, "overwrite")
	ctx = withFilePluginOverride(ctx, req)
	if auth, ok := fileAuthFromContext(ctx); ok {
		svc, conditionalCtx, svcErr := conditionalFileService(ctx, t.svc, req, auth, project, fromPath, toPath, files.FileOperationRename)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		result, svcErr := svc.Rename(conditionalCtx, auth, project, fromPath, toPath, overwrite)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		toolResult, encodeErr := mcp.NewToolResultJSON(map[string]any{"moved_count": result.MovedCount})
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil //nolint:nilerr // error is encoded in the MCP tool result
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
