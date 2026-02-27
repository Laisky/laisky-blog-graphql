package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Laisky/zap"
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
		mcp.WithString("project", mcp.Description("Target project namespace. Defaults to `default` when omitted.")),
		mcp.WithString("session_id", mcp.Description("Session identifier. Defaults to `default` when omitted.")),
		mcp.WithString("turn_id", mcp.Description("Turn identifier. Auto-generated when omitted.")),
		mcp.WithString("user_id", mcp.Description("Optional user identifier.")),
		mcp.WithArray(
			"current_input",
			mcp.Description("Current turn input items in Responses API format."),
			mcp.Required(),
			mcp.Items(memoryResponseItemSchema()),
		),
		mcp.WithString("base_instructions", mcp.Description("Optional base system instructions.")),
		mcp.WithNumber("max_input_tok", mcp.Description("Optional max context token budget. Defaults to 120000 when omitted.")),
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
	applyMemoryDefaultsBeforeTurn(&request)

	response, err := tool.service.BeforeTurn(ctx, auth, request)
	if err != nil {
		logger := fileToolLoggerFromContext(ctx)
		typedErr, typedErrOK := mcpmemory.AsError(err)
		errorCode := ""
		retryable := false
		message := strings.TrimSpace(err.Error())
		if typedErrOK {
			errorCode = string(typedErr.Code)
			retryable = typedErr.Retryable
			if strings.TrimSpace(typedErr.Message) != "" {
				message = strings.TrimSpace(typedErr.Message)
			}
		}
		logger.Debug("memory_before_turn failed",
			zap.String("project", request.Project),
			zap.String("session_id", request.SessionID),
			zap.String("turn_id", request.TurnID),
			zap.String("user_identity", auth.UserIdentity),
			zap.String("error_type", fmt.Sprintf("%T", err)),
			zap.String("error_code", errorCode),
			zap.Bool("retryable", retryable),
			zap.String("error_message", message),
		)
		return memoryToolErrorFromErr(err), nil
	}

	result, err := mcp.NewToolResultJSON(response)
	if err != nil {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "failed to encode response", true), nil
	}
	return result, nil
}
