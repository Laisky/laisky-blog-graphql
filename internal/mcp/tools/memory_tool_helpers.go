package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

const (
	defaultMemoryProject   = "default"
	defaultMemorySessionID = "default"
	defaultMaxInputTok     = 120000
	defaultListDepth       = 8
	defaultListLimit       = 200
)

// memoryResponseItemSchema returns a permissive JSON schema for one Responses-style item.
func memoryResponseItemSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
}

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

// applyMemoryDefaultsBeforeTurn fills optional memory_before_turn fields with defaults.
func applyMemoryDefaultsBeforeTurn(request *mcpmemory.BeforeTurnRequest) {
	request.Project = normalizeMemoryStringDefault(request.Project, defaultMemoryProject)
	request.SessionID = normalizeMemoryStringDefault(request.SessionID, defaultMemorySessionID)
	request.TurnID = normalizeMemoryTurnID(request.TurnID)
	if request.MaxInputTok <= 0 {
		request.MaxInputTok = defaultMaxInputTok
	}
}

// applyMemoryDefaultsAfterTurn fills optional memory_after_turn fields with defaults.
func applyMemoryDefaultsAfterTurn(request *mcpmemory.AfterTurnRequest) {
	request.Project = normalizeMemoryStringDefault(request.Project, defaultMemoryProject)
	request.SessionID = normalizeMemoryStringDefault(request.SessionID, defaultMemorySessionID)
	request.TurnID = normalizeMemoryTurnID(request.TurnID)
}

// applyMemoryDefaultsSession fills optional session-scoped request fields with defaults.
func applyMemoryDefaultsSession(request *mcpmemory.SessionRequest) {
	request.Project = normalizeMemoryStringDefault(request.Project, defaultMemoryProject)
	request.SessionID = normalizeMemoryStringDefault(request.SessionID, defaultMemorySessionID)
}

// applyMemoryDefaultsListDir fills optional list_dir_with_abstract fields with defaults.
func applyMemoryDefaultsListDir(request *mcpmemory.ListDirWithAbstractRequest) {
	request.Project = normalizeMemoryStringDefault(request.Project, defaultMemoryProject)
	request.SessionID = normalizeMemoryStringDefault(request.SessionID, defaultMemorySessionID)
	if request.Depth <= 0 {
		request.Depth = defaultListDepth
	}
	if request.Limit <= 0 {
		request.Limit = defaultListLimit
	}
}

// normalizeMemoryStringDefault trims value and returns fallback when empty.
func normalizeMemoryStringDefault(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}

	return trimmed
}

// normalizeMemoryTurnID trims turnID and generates a default one when empty.
func normalizeMemoryTurnID(turnID string) string {
	trimmed := strings.TrimSpace(turnID)
	if trimmed != "" {
		return trimmed
	}

	return generateMemoryTurnID(time.Now().UTC())
}

// generateMemoryTurnID builds a monotonic turn identifier with short random suffix.
func generateMemoryTurnID(now time.Time) string {
	return "turn-" + strconv.FormatInt(now.UTC().UnixMilli(), 10) + "-" + generateMemoryTurnIDSuffix()
}

// generateMemoryTurnIDSuffix creates a short random lowercase hex suffix.
func generateMemoryTurnIDSuffix() string {
	randomBytes := make([]byte, 3)
	if _, err := rand.Read(randomBytes); err == nil {
		return hex.EncodeToString(randomBytes)
	}

	fallback := time.Now().UTC().UnixNano()
	return hex.EncodeToString([]byte{byte(fallback), byte(fallback >> 8), byte(fallback >> 16)})
}
