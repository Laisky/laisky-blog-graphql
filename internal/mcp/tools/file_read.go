package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// FileReadTool implements the file_read MCP tool.
type FileReadTool struct {
	svc FileService
}

// NewFileReadTool constructs a FileReadTool.
func NewFileReadTool(svc FileService) (*FileReadTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileReadTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_read.
func (t *FileReadTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"file_read",
		mcp.WithDescription("Read file content with optional byte offsets."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File path to read.")),
		mcp.WithNumber("offset", mcp.Description("Byte offset to start reading from.")),
		mcp.WithNumber("length", mcp.Description("Number of bytes to read; -1 reads to EOF.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes the file_read tool logic.
func (t *FileReadTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	offset := readInt64Arg(req, "offset")
	length := readInt64ArgWithDefault(req, "length", -1)
	if auth, ok := fileAuthFromContext(ctx); ok {
		result, svcErr := t.svc.Read(ctx, auth, project, path, offset, length)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil
		}
		payload := map[string]any{
			"content":          result.Content,
			"content_encoding": result.ContentEncoding,
		}
		toolResult, encodeErr := mcp.NewToolResultJSON(payload)
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}
