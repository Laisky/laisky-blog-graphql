package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func (s *Server) handleWebSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.webSearch == nil {
		result := mcp.NewToolResultError("web search is not configured")
		s.recordToolInvocation(ctx, "web_search", apiKey, args, time.Now().UTC(), 0, oneapi.PriceWebSearch.Int(), result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.webSearch.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "web_search", apiKey, args, start, duration, oneapi.PriceWebSearch.Int(), result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

// handleWebFetch executes the web_fetch MCP tool. The context carries request metadata,
// and the request supplies the target URL. It returns a structured response when the
// fetch succeeds or a tool error when processing fails.
func (s *Server) handleWebFetch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.webFetch == nil {
		result := mcp.NewToolResultError("web fetch is not configured")
		s.recordToolInvocation(ctx, "web_fetch", apiKey, args, time.Now().UTC(), 0, oneapi.PriceWebFetch.Int(), result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.webFetch.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "web_fetch", apiKey, args, start, duration, oneapi.PriceWebFetch.Int(), result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func extractAPIKey(authHeader string) string {
	return library.StripBearerPrefix(authHeader)
}

func apiKeyFromContext(ctx context.Context) string {
	authHeader, _ := ctx.Value(keyAuthorization).(string)
	return extractAPIKey(authHeader)
}

// LoggerFromContext retrieves the per-request logger from the MCP context.
// Falls back to a shared logger if none is present in context.
func LoggerFromContext(ctx context.Context) logSDK.Logger {
	if logger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && logger != nil {
		return logger
	}
	if logger, ok := ctx.Value(keyLogger).(logSDK.Logger); ok && logger != nil {
		return logger
	}
	return log.Logger.Named("mcp_fallback")
}

func (s *Server) recordToolInvocation(ctx context.Context, toolName string, apiKey string, args map[string]any, startedAt time.Time, duration time.Duration, baseCost int, result *mcp.CallToolResult, invokeErr error) {
	if s.callLogger == nil {
		if s.logger != nil {
			s.logger.Debug("call logger is nil, skipping record", zap.String("tool", toolName))
		}
		return
	}

	params := cloneArguments(args)
	params = files.RedactToolArguments(toolName, params)
	status := calllog.StatusSuccess
	errorMessage := ""

	if invokeErr != nil {
		status = calllog.StatusError
		errorMessage = invokeErr.Error()
	}

	if result != nil && result.IsError {
		status = calllog.StatusError
		if msg := toolErrorMessage(result); msg != "" {
			if errorMessage == "" {
				errorMessage = msg
			} else {
				errorMessage = fmt.Sprintf("%s | %s", errorMessage, msg)
			}
		}
	}

	costValue := 0
	if status == calllog.StatusSuccess {
		costValue = baseCost
	}

	if duration < 0 {
		duration = 0
	}

	occurredAt := startedAt.UTC()
	if startedAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	input := calllog.RecordInput{
		ToolName:     toolName,
		APIKey:       apiKey,
		Status:       status,
		Cost:         costValue,
		Duration:     duration,
		Parameters:   params,
		ErrorMessage: errorMessage,
		OccurredAt:   occurredAt,
	}

	if err := s.callLogger.Record(ctx, input); err != nil {
		logger := s.logger
		if logger == nil {
			logger = log.Logger.Named("mcp")
		}
		logger.Warn("record call log", zap.Error(err), zap.String("tool", toolName))
	}
}

func cloneArguments(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

func argumentsMap(raw any) map[string]any {
	switch value := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return value
	case map[string]string:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = item
		}
		return result
	default:
		return map[string]any{"value": value}
	}
}

func toolErrorMessage(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	if !result.IsError {
		return ""
	}
	for _, content := range result.Content {
		if textContent, ok := mcp.AsTextContent(content); ok {
			txt := strings.TrimSpace(textContent.Text)
			if txt != "" {
				return txt
			}
		}
	}
	if result.StructuredContent != nil {
		return fmt.Sprint(result.StructuredContent)
	}
	return ""
}

func (s *Server) handleAskUser(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.askUser == nil {
		result := mcp.NewToolResultError("ask_user tool is not available")
		s.recordToolInvocation(ctx, "ask_user", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.askUser.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "ask_user", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleGetUserRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.getUserRequest == nil {
		result := mcp.NewToolResultError("get_user_request tool is not available")
		s.recordToolInvocation(ctx, "get_user_request", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.getUserRequest.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "get_user_request", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleExtractKeyInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.extractKeyInfo == nil {
		result := mcp.NewToolResultError("extract_key_info tool is not available")
		s.recordToolInvocation(ctx, "extract_key_info", apiKey, args, time.Now().UTC(), 0, oneapi.PriceExtractKeyInfo.Int(), result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.extractKeyInfo.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "extract_key_info", apiKey, args, start, duration, oneapi.PriceExtractKeyInfo.Int(), result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileStat(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileStat == nil {
		result := mcp.NewToolResultError("file_stat tool is not available")
		s.recordToolInvocation(ctx, "file_stat", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileStat.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_stat", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileRead == nil {
		result := mcp.NewToolResultError("file_read tool is not available")
		s.recordToolInvocation(ctx, "file_read", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileRead.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_read", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileWrite == nil {
		result := mcp.NewToolResultError("file_write tool is not available")
		s.recordToolInvocation(ctx, "file_write", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileWrite.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_write", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileDelete == nil {
		result := mcp.NewToolResultError("file_delete tool is not available")
		s.recordToolInvocation(ctx, "file_delete", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileDelete.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_delete", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileRename(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileRename == nil {
		result := mcp.NewToolResultError("file_rename tool is not available")
		s.recordToolInvocation(ctx, "file_rename", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileRename.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_rename", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileList == nil {
		result := mcp.NewToolResultError("file_list tool is not available")
		s.recordToolInvocation(ctx, "file_list", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileList.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_list", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

func (s *Server) handleFileSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.fileSearch == nil {
		result := mcp.NewToolResultError("file_search tool is not available")
		s.recordToolInvocation(ctx, "file_search", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.fileSearch.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "file_search", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}

// handleMCPPipe executes the mcp_pipe MCP tool, auditing the invocation via the call logger.
func (s *Server) handleMCPPipe(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	if s.mcpPipe == nil {
		result := mcp.NewToolResultError("mcp_pipe tool is not available")
		s.recordToolInvocation(ctx, "mcp_pipe", apiKey, args, time.Now().UTC(), 0, 0, result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := s.mcpPipe.Handle(ctx, req)
	duration := time.Since(start)
	s.recordToolInvocation(ctx, "mcp_pipe", apiKey, args, start, duration, 0, result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}
	return result, nil
}
