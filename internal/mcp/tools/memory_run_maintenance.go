package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// MemoryRunMaintenanceTool implements the memory_run_maintenance MCP tool.
type MemoryRunMaintenanceTool struct {
	service MemoryService
}

// NewMemoryRunMaintenanceTool creates a memory_run_maintenance tool.
func NewMemoryRunMaintenanceTool(service MemoryService) (*MemoryRunMaintenanceTool, error) {
	if service == nil {
		return nil, mcpmemory.NewError(mcpmemory.ErrCodeInternal, "memory service is required", false)
	}
	return &MemoryRunMaintenanceTool{service: service}, nil
}

// Definition returns MCP metadata for memory_run_maintenance.
func (tool *MemoryRunMaintenanceTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"memory_run_maintenance",
		mcp.WithDescription("Run compaction, retention sweep, and summary refresh for one memory session."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session identifier.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes memory_run_maintenance.
func (tool *MemoryRunMaintenanceTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := memoryAuthFromContext(ctx)
	if !ok {
		return memoryToolErrorResult(mcpmemory.ErrCodePermissionDenied, "missing authorization", false), nil
	}

	request := mcpmemory.SessionRequest{}
	if err := decodeMemoryRequest(req, &request); err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInvalidArgument, "invalid request payload", false), nil
	}

	err := tool.service.RunMaintenance(ctx, auth, request)
	if err != nil {
		return memoryToolErrorFromErr(err), nil
	}

	result, err := mcp.NewToolResultJSON(map[string]any{"ok": true})
	if err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "failed to encode response", true), nil
	}
	return result, nil
}
