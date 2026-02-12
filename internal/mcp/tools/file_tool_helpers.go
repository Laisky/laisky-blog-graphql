package tools

import (
	"context"

	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// fileAuthFromContext extracts the trusted auth context for file tools.
func fileAuthFromContext(ctx context.Context) (files.AuthContext, bool) {
	value, ok := ctx.Value(ctxkeys.AuthContext).(*files.AuthContext)
	if !ok || value == nil {
		return files.AuthContext{}, false
	}
	return *value, true
}

// fileToolLoggerFromContext returns a request-scoped logger when available.
// It accepts a context and returns the logger to use for tool diagnostics.
func fileToolLoggerFromContext(ctx context.Context) logSDK.Logger {
	if ctxLogger := gmw.GetLogger(ctx); ctxLogger != nil {
		return ctxLogger
	}
	if ctxLogger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && ctxLogger != nil {
		return ctxLogger
	}

	return log.Logger.Named("mcp_file_tools")
}

// fileToolErrorResult builds a structured MCP error response for file tools.
func fileToolErrorResult(code files.ErrorCode, message string, retryable bool) *mcp.CallToolResult {
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

// fileToolErrorFromErr converts service errors into tool responses.
func fileToolErrorFromErr(err error) *mcp.CallToolResult {
	if err == nil {
		return nil
	}
	if typed, ok := files.AsError(err); ok {
		return fileToolErrorResult(typed.Code, typed.Message, typed.Retryable)
	}
	return fileToolErrorResult(files.ErrCodeSearchBackend, "internal error", true)
}

// isFileErrorCode reports whether an error chain contains the specified file error code.
func isFileErrorCode(err error, code files.ErrorCode) bool {
	if err == nil {
		return false
	}
	typed, ok := files.AsError(err)
	if !ok {
		return false
	}
	return typed.Code == code
}

// readStringArg extracts an optional string argument from the request.
func readStringArg(req mcp.CallToolRequest, key string) string {
	if req.Params.Arguments == nil {
		return ""
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		if value, ok := raw[key].(string); ok {
			return value
		}
	}
	return ""
}

// readIntArg extracts an optional integer argument from the request.
func readIntArg(req mcp.CallToolRequest, key string) int {
	if req.Params.Arguments == nil {
		return 0
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		switch value := raw[key].(type) {
		case int:
			return value
		case int64:
			return int(value)
		case float64:
			return int(value)
		}
	}
	return 0
}

// readIntArgWithDefault extracts an optional int argument with a default fallback.
func readIntArgWithDefault(req mcp.CallToolRequest, key string, def int) int {
	if req.Params.Arguments == nil {
		return def
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		if _, exists := raw[key]; !exists {
			return def
		}
		switch value := raw[key].(type) {
		case int:
			return value
		case int64:
			return int(value)
		case float64:
			return int(value)
		}
	}
	return def
}

// readInt64Arg extracts an optional int64 argument from the request.
func readInt64Arg(req mcp.CallToolRequest, key string) int64 {
	if req.Params.Arguments == nil {
		return 0
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		switch value := raw[key].(type) {
		case int:
			return int64(value)
		case int64:
			return value
		case float64:
			return int64(value)
		}
	}
	return 0
}

// readInt64ArgWithDefault extracts an optional int64 argument with a default fallback.
func readInt64ArgWithDefault(req mcp.CallToolRequest, key string, def int64) int64 {
	if req.Params.Arguments == nil {
		return def
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		if _, exists := raw[key]; !exists {
			return def
		}
		switch value := raw[key].(type) {
		case int:
			return int64(value)
		case int64:
			return value
		case float64:
			return int64(value)
		}
	}
	return def
}

// readBoolArg extracts an optional bool argument from the request.
func readBoolArg(req mcp.CallToolRequest, key string) bool {
	if req.Params.Arguments == nil {
		return false
	}
	if raw, ok := req.Params.Arguments.(map[string]any); ok {
		if value, ok := raw[key].(bool); ok {
			return value
		}
	}
	return false
}
