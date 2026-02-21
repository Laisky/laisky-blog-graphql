package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// MemoryListDirWithAbstractTool implements the memory_list_dir_with_abstract MCP tool.
type MemoryListDirWithAbstractTool struct {
	service MemoryService
}

// NewMemoryListDirWithAbstractTool creates a memory_list_dir_with_abstract tool.
func NewMemoryListDirWithAbstractTool(service MemoryService) (*MemoryListDirWithAbstractTool, error) {
	if service == nil {
		return nil, mcpmemory.NewError(mcpmemory.ErrCodeInternal, "memory service is required", false)
	}
	return &MemoryListDirWithAbstractTool{service: service}, nil
}

// Definition returns MCP metadata for memory_list_dir_with_abstract.
func (tool *MemoryListDirWithAbstractTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"memory_list_dir_with_abstract",
		mcp.WithDescription("List memory directories and include abstract/overview metadata."),
		mcp.WithString("project", mcp.Description("Target project namespace. Defaults to `default` when omitted.")),
		mcp.WithString("session_id", mcp.Description("Session identifier. Defaults to `default` when omitted.")),
		mcp.WithString("path", mcp.Description("Directory path relative to session root.")),
		mcp.WithNumber("depth", mcp.Description("Directory traversal depth. Defaults to 8 when omitted.")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of entries returned. Defaults to 200 when omitted.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes memory_list_dir_with_abstract.
func (tool *MemoryListDirWithAbstractTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := memoryAuthFromContext(ctx)
	if !ok {
		return memoryToolErrorResult(mcpmemory.ErrCodePermissionDenied, "missing authorization", false), nil
	}

	request := mcpmemory.ListDirWithAbstractRequest{
		Depth: defaultListDepth,
		Limit: defaultListLimit,
	}
	if err := decodeMemoryRequest(req, &request); err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInvalidArgument, "invalid request payload", false), nil
	}
	applyMemoryDefaultsListDir(&request)

	response, err := tool.service.ListDirWithAbstract(ctx, auth, request)
	if err != nil {
		return memoryToolErrorFromErr(err), nil
	}

	result, err := mcp.NewToolResultJSON(response)
	if err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "failed to encode response", true), nil
	}
	return result, nil
}
