package tools

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// MemoryService exposes memory lifecycle operations for MCP tools.
type MemoryService interface {
	BeforeTurn(context.Context, files.AuthContext, mcpmemory.BeforeTurnRequest) (mcpmemory.BeforeTurnResponse, error)
	AfterTurn(context.Context, files.AuthContext, mcpmemory.AfterTurnRequest) error
	RunMaintenance(context.Context, files.AuthContext, mcpmemory.SessionRequest) error
	ListDirWithAbstract(context.Context, files.AuthContext, mcpmemory.ListDirWithAbstractRequest) (mcpmemory.ListDirWithAbstractResponse, error)
}

// memoryAuthFromContext extracts memory auth from request context.
func memoryAuthFromContext(ctx context.Context) (files.AuthContext, bool) {
	auth, ok := fileAuthFromContext(ctx)
	if !ok {
		return files.AuthContext{}, false
	}
	return auth, true
}

// decodeMemoryRequest decodes tool arguments into request DTO.
func decodeMemoryRequest(req mcp.CallToolRequest, out any) error {
	if req.Params.Arguments == nil {
		data, marshalErr := json.Marshal(map[string]any{})
		if marshalErr != nil {
			return marshalErr
		}
		return json.Unmarshal(data, out)
	}

	data, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// memoryToolErrorResult builds structured tool errors for memory tools.
func memoryToolErrorResult(code mcpmemory.ErrorCode, message string, retryable bool) *mcp.CallToolResult {
	payload := map[string]any{
		"code":      string(code),
		"message":   message,
		"retryable": retryable,
	}
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return mcp.NewToolResultError(message)
	}
	result.IsError = true
	return result
}

// memoryToolErrorFromErr maps service errors to structured tool results.
func memoryToolErrorFromErr(err error) *mcp.CallToolResult {
	if err == nil {
		return nil
	}
	typed, ok := mcpmemory.AsError(err)
	if !ok {
		return memoryToolErrorResult(mcpmemory.ErrCodeInternal, "internal error", true)
	}
	return memoryToolErrorResult(typed.Code, typed.Message, typed.Retryable)
}
