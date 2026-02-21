package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// MemoryBeforeTurnTool implements the memory_before_turn MCP tool.
type MemoryBeforeTurnTool struct {
	service MemoryService
}

// NewMemoryBeforeTurnTool creates a memory_before_turn tool.
func NewMemoryBeforeTurnTool(service MemoryService) (*MemoryBeforeTurnTool, error) {
	if service == nil {
		return nil, mcpmemory.NewError(mcpmemory.ErrCodeInternal, "memory service is required", false)
	}
	return &MemoryBeforeTurnTool{service: service}, nil
}

// Definition returns MCP metadata for memory_before_turn.
func (tool *MemoryBeforeTurnTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"memory_before_turn",
		mcp.WithDescription("Prepare model input with recalled memory context for the current turn."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session identifier.")),
		mcp.WithString("turn_id", mcp.Required(), mcp.Description("Turn identifier.")),
		mcp.WithString("user_id", mcp.Description("Optional user identifier.")),
		mcp.WithArray("current_input", mcp.Description("Current turn input items in Responses API format.")),
		mcp.WithString("base_instructions", mcp.Description("Optional base system instructions.")),
		mcp.WithNumber("max_input_tok", mcp.Description("Optional max context token budget.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
	)
}

// Handle executes memory_before_turn.
func (tool *MemoryBeforeTurnTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := memoryAuthFromContext(ctx)
	if !ok {
		return memoryToolErrorResult(mcpmemory.ErrCodePermissionDenied, "missing authorization", false), nil
	}

	request := mcpmemory.BeforeTurnRequest{}
	if err := decodeMemoryRequest(req, &request); err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInvalidArgument, "invalid request payload", false), nil
	}

	response, err := tool.service.BeforeTurn(ctx, auth, request)
	if err != nil {
		return memoryToolErrorFromErr(err), nil
	}

	result, err := mcp.NewToolResultJSON(response)
	if err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "failed to encode response", true), nil
	}
	return result, nil
}
