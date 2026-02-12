package tools

import (
	"context"

	"github.com/Laisky/zap"
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
	logger := fileToolLoggerFromContext(ctx).Named("file_list")

	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	rawPath := readStringArg(req, "path")
	path := normalizeFileListPath(rawPath)
	depth := readIntArgWithDefault(req, "depth", 1)
	limit := readIntArg(req, "limit")
	logger.Debug("file_list request parsed",
		zap.String("path_raw", rawPath),
		zap.String("path_normalized", path),
		zap.Int("depth", depth),
		zap.Int("limit", limit),
	)
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.List(ctx, auth, project, path, depth, limit)
		if svcErr != nil {
			if typed, typeOK := files.AsError(svcErr); typeOK {
				logger.Debug("file_list service error",
					zap.String("path_raw", rawPath),
					zap.String("path_normalized", path),
					zap.Int("depth", depth),
					zap.Int("limit", limit),
					zap.String("code", string(typed.Code)),
					zap.Bool("retryable", typed.Retryable),
				)
			} else {
				logger.Debug("file_list service error",
					zap.String("path_raw", rawPath),
					zap.String("path_normalized", path),
					zap.Int("depth", depth),
					zap.Int("limit", limit),
					zap.Error(svcErr),
				)
			}
			if path == "" && isFileErrorCode(svcErr, files.ErrCodeNotFound) {
				emptyPayload := map[string]any{"entries": []files.FileEntry{}, "has_more": false}
				emptyResult, encodeErr := mcp.NewToolResultJSON(emptyPayload)
				if encodeErr != nil {
					return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
				}
				return emptyResult, nil
			}
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

// normalizeFileListPath canonicalizes file_list directory paths.
// It accepts a raw path argument and returns the normalized service path.
func normalizeFileListPath(path string) string {
	if path == "/" {
		return ""
	}

	return path
}
