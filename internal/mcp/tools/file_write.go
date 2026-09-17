package tools

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileWriteTool implements the file_write MCP tool.
type FileWriteTool struct{ svc FileService }

// NewFileWriteTool constructs a FileWriteTool.
func NewFileWriteTool(svc FileService) (*FileWriteTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileWriteTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_write.
func (t *FileWriteTool) Definition() mcp.Tool {
	return mcp.NewTool("file_write",
		mcp.WithDescription("Write UTF-8 file content and return its committed version. Use expected_version for edits based on a prior read, or create_only for creation. Blind APPEND is not retry-idempotent."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File path to write.")),
		mcp.WithString("content", mcp.Required(), mcp.Description("UTF-8 encoded content.")),
		mcp.WithString("content_encoding", mcp.Description("Content encoding; must be utf-8.")),
		mcp.WithNumber("offset", mcp.Description("Byte offset for overwrite mode; must not split a UTF-8 character.")),
		mcp.WithString("mode", mcp.Description("Write mode: APPEND, OVERWRITE, or TRUNCATE.")),
		expectedFileVersionOption(),
		mcp.WithBoolean("create_only", mcp.Description("Require no active file at this path. Mutually exclusive with expected_version.")),
		fileToolPluginOption(), mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the file_write tool logic.
func (t *FileWriteTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path = normalizeFilePath(path)
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	encoding := readStringArg(req, "content_encoding")
	mode := files.WriteMode(strings.ToUpper(readStringArg(req, "mode")))
	offset := readInt64Arg(req, "offset")
	ctx = withFilePluginOverride(ctx, req)
	if auth, ok := fileAuthFromContext(ctx); ok {
		svc, conditionalCtx, svcErr := conditionalFileService(ctx, t.svc, req, auth, project, path, "", files.FileOperationWrite)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		result, svcErr := svc.Write(conditionalCtx, auth, project, path, content, encoding, offset, mode)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		payload := map[string]any{"bytes_written": result.BytesWritten}
		addFileVersion(payload, result.Version)
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil //nolint:nilerr // error is encoded in the MCP tool result
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
