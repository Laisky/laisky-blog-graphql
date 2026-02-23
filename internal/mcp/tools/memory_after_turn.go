package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// MemoryAfterTurnTool implements the memory_after_turn MCP tool.
type MemoryAfterTurnTool struct {
	service MemoryService
}

// NewMemoryAfterTurnTool creates a memory_after_turn tool.
func NewMemoryAfterTurnTool(service MemoryService) (*MemoryAfterTurnTool, error) {
	if service == nil {
		return nil, mcpmemory.NewError(mcpmemory.ErrCodeInternal, "memory service is required", false)
	}
	return &MemoryAfterTurnTool{service: service}, nil
}

// Definition returns MCP metadata for memory_after_turn.
func (tool *MemoryAfterTurnTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"memory_after_turn",
		mcp.WithDescription("Persist turn artifacts and update memory tiers after model response."),
		mcp.WithString("project", mcp.Description("Target project namespace. Defaults to `default` when omitted.")),
		mcp.WithString("session_id", mcp.Description("Session identifier. Defaults to `default` when omitted.")),
		mcp.WithString("turn_id", mcp.Description("Turn identifier. Auto-generated when omitted.")),
		mcp.WithString("user_id", mcp.Description("Optional user identifier.")),
		mcp.WithArray(
			"input_items",
			mcp.Description("Prepared turn input items."),
			mcp.Items(memoryResponseItemSchema()),
		),
		mcp.WithArray(
			"output_items",
			mcp.Description("Model output items."),
			mcp.Items(memoryResponseItemSchema()),
		),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes memory_after_turn.
func (tool *MemoryAfterTurnTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := memoryAuthFromContext(ctx)
	if !ok {
		return memoryToolErrorResult(mcpmemory.ErrCodePermissionDenied, "missing authorization", false), nil
	}

	request := mcpmemory.AfterTurnRequest{}
	if err := decodeMemoryRequest(req, &request); err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInvalidArgument, "invalid request payload", false), nil
	}
	applyMemoryDefaultsAfterTurn(&request)

	err := tool.service.AfterTurn(ctx, auth, request)
	if err != nil {
		return memoryToolErrorFromErr(err), nil
	}

	result, err := mcp.NewToolResultJSON(map[string]any{"ok": true})
	if err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "failed to encode response", true), nil
	}
	return result, nil
}
