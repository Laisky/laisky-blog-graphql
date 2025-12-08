package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

type ctxKey string

const (
	keyAuthorization ctxKey = "authorization"
	keyLogger        ctxKey = "logger"
	httpLogBodyLimit int    = 4096
)

type callRecorder interface {
	Record(context.Context, calllog.RecordInput) error
}

// Server wraps the MCP server state for the HTTP transport.
type Server struct {
	handler        http.Handler
	logger         logSDK.Logger
	webSearch      *tools.WebSearchTool
	webFetch       *tools.WebFetchTool
	askUser        *tools.AskUserTool
	getUserRequest *tools.GetUserRequestTool
	extractKeyInfo *tools.ExtractKeyInfoTool
	callLogger     callRecorder
	holdManager    *userrequests.HoldManager
}

// NewServer constructs an MCP HTTP server.
// searchProvider enables the web_search tool when not nil and toolsSettings.WebSearchEnabled is true.
// askUserService enables the ask_user tool when not nil and toolsSettings.AskUserEnabled is true.
// rdb enables the web_fetch tool when not nil and toolsSettings.WebFetchEnabled is true.
// userRequestService enables the get_user_request tool when not nil and toolsSettings.GetUserRequestEnabled is true.
// ragService enables the extract_key_info tool when not nil and toolsSettings.ExtractKeyInfoEnabled is true.
// callLogger records tool invocations for auditing when provided.
// logger overrides the default logger when provided.
// It returns the configured server or an error if no capability is available.
func NewServer(searchProvider searchlib.Provider, askUserService *askuser.Service, userRequestService *userrequests.Service, ragService *rag.Service, ragSettings rag.Settings, rdb *rlibs.DB, callLogger callRecorder, toolsSettings ToolsSettings, logger logSDK.Logger) (*Server, error) {
	if searchProvider == nil && askUserService == nil && userRequestService == nil && ragService == nil && rdb == nil {
		return nil, errors.New("at least one MCP capability must be enabled")
	}
	if logger == nil {
		logger = log.Logger
	}

	hooks := newMCPHooks(logger.Named("mcp_hooks"))

	mcpServer := srv.NewMCPServer(
		"LAISKY MCP SERVER",
		"1.0.0",
		srv.WithToolCapabilities(true),
		srv.WithInstructions("Use web_search for Google Programmable Search queries and web_fetch to retrieve dynamic web pages."),
		srv.WithRecovery(),
		srv.WithHooks(hooks),
	)

	serverLogger := logger.Named("mcp")

	streamable := srv.NewStreamableHTTPServer(
		mcpServer,
		srv.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			// Inject authorization header
			ctx = context.WithValue(ctx, keyAuthorization, r.Header.Get("Authorization"))

			// Create a per-request logger with request-specific context
			reqLogger := serverLogger.With(
				zap.String("request_id", gutils.UUID7()),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			ctx = context.WithValue(ctx, keyLogger, reqLogger)
			ctx = context.WithValue(ctx, ctxkeys.Logger, reqLogger)
			return ctx
		}),
	)

	s := &Server{
		handler:    withHTTPLogging(streamable, serverLogger.Named("http")),
		logger:     serverLogger,
		callLogger: callLogger,
	}

	apiKeyProvider := func(ctx context.Context) string {
		authHeader, _ := ctx.Value(keyAuthorization).(string)
		return extractAPIKey(authHeader)
	}
	headerProvider := func(ctx context.Context) string {
		authHeader, _ := ctx.Value(keyAuthorization).(string)
		return authHeader
	}

	if searchProvider != nil && toolsSettings.WebSearchEnabled {
		webSearchTool, err := tools.NewWebSearchTool(
			searchProvider,
			serverLogger.Named("web_search"),
			apiKeyProvider,
			oneapi.CheckUserExternalBilling,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init web_search tool")
		}
		s.webSearch = webSearchTool
		mcpServer.AddTool(webSearchTool.Definition(), s.handleWebSearch)
	} else if searchProvider != nil && !toolsSettings.WebSearchEnabled {
		serverLogger.Info("web_search tool disabled by configuration")
	}

	if rdb != nil && toolsSettings.WebFetchEnabled {
		webFetchTool, err := tools.NewWebFetchTool(
			rdb,
			serverLogger.Named("web_fetch"),
			apiKeyProvider,
			oneapi.CheckUserExternalBilling,
			searchlib.FetchDynamicURLContent,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init web_fetch tool")
		}
		s.webFetch = webFetchTool
		mcpServer.AddTool(webFetchTool.Definition(), s.handleWebFetch)
	} else if rdb != nil && !toolsSettings.WebFetchEnabled {
		serverLogger.Info("web_fetch tool disabled by configuration")
	}

	if askUserService != nil && toolsSettings.AskUserEnabled {
		askUserTool, err := tools.NewAskUserTool(
			askUserService,
			serverLogger.Named("ask_user"),
			headerProvider,
			askuser.ParseAuthorizationContext,
			0,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init ask_user tool")
		}
		s.askUser = askUserTool
		mcpServer.AddTool(askUserTool.Definition(), s.handleAskUser)
	} else if askUserService != nil && !toolsSettings.AskUserEnabled {
		serverLogger.Info("ask_user tool disabled by configuration")
	}

	if userRequestService != nil && toolsSettings.GetUserRequestEnabled {
		// Create HoldManager for this user request service
		holdMgr := userrequests.NewHoldManager(userRequestService, serverLogger.Named("hold_manager"), nil)
		s.holdManager = holdMgr

		getUserRequestTool, err := tools.NewGetUserRequestTool(
			userRequestService,
			holdMgr,
			serverLogger.Named("get_user_request"),
			headerProvider,
			askuser.ParseAuthorizationContext,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init get_user_request tool")
		}
		s.getUserRequest = getUserRequestTool
		mcpServer.AddTool(getUserRequestTool.Definition(), s.handleGetUserRequest)
	} else if userRequestService != nil && !toolsSettings.GetUserRequestEnabled {
		serverLogger.Info("get_user_request tool disabled by configuration")
	}

	if ragService != nil && toolsSettings.ExtractKeyInfoEnabled {
		ragTool, err := tools.NewExtractKeyInfoTool(
			ragService,
			serverLogger.Named("extract_key_info"),
			headerProvider,
			oneapi.CheckUserExternalBilling,
			ragSettings,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init extract_key_info tool")
		}
		s.extractKeyInfo = ragTool
		mcpServer.AddTool(ragTool.Definition(), s.handleExtractKeyInfo)
	} else if ragService != nil && !toolsSettings.ExtractKeyInfoEnabled {
		serverLogger.Info("extract_key_info tool disabled by configuration")
	}

	return s, nil
}

// Handler returns the HTTP handler that should be mounted to serve MCP traffic.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// HoldManager returns the HoldManager instance for the user requests feature.
// Returns nil if the get_user_request tool is not enabled.
func (s *Server) HoldManager() *userrequests.HoldManager {
	return s.holdManager
}

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
	return result, err
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
	return result, err
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
	return result, err
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
	return result, err
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
	return result, err
}

func newMCPHooks(logger logSDK.Logger) *srv.Hooks {
	if logger == nil {
		return nil
	}

	hooks := &srv.Hooks{}

	hooks.AddBeforeAny(func(ctx context.Context, id any, method mcp.MCPMethod, message any) {
		fields := hookLogFields(ctx, id, method)
		if message != nil {
			fields = append(fields, zap.Any("request", message))
		}
		logger.Debug("mcp request received", fields...)
	})

	hooks.AddOnSuccess(func(ctx context.Context, id any, method mcp.MCPMethod, message any, result any) {
		fields := hookLogFields(ctx, id, method)
		if result != nil {
			fields = append(fields, zap.Any("response", result))
		}
		logger.Info("mcp request succeeded", fields...)
	})

	hooks.AddOnError(func(ctx context.Context, id any, method mcp.MCPMethod, message any, err error) {
		fields := hookLogFields(ctx, id, method)
		if message != nil {
			fields = append(fields, zap.Any("request", message))
		}
		fields = append(fields, zap.Error(err))
		logger.Error("mcp request failed", fields...)
	})

	hooks.AddOnRegisterSession(func(ctx context.Context, session srv.ClientSession) {
		logger.Info("mcp session registered", zap.String("session_id", session.SessionID()))
	})

	hooks.AddOnUnregisterSession(func(ctx context.Context, session srv.ClientSession) {
		logger.Info("mcp session unregistered", zap.String("session_id", session.SessionID()))
	})

	return hooks
}

func hookLogFields(ctx context.Context, id any, method mcp.MCPMethod) []zap.Field {
	fields := []zap.Field{
		zap.Any("request_id", id),
		zap.String("method", string(method)),
	}

	if session := srv.ClientSessionFromContext(ctx); session != nil {
		fields = append(fields, zap.String("session_id", session.SessionID()))
	}

	return fields
}

func withHTTPLogging(next http.Handler, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	if logger == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startAt := time.Now()
		body, truncated, err := readAndRestoreRequestBody(r, httpLogBodyLimit)
		if err != nil {
			logger.Error("read request body", zap.Error(err))
		}

		logger.Debug("incoming http request",
			zap.String("method", r.Method),
			zap.String("url", r.URL.String()),
			zap.String("body", body),
			zap.Bool("body_truncated", truncated),
			zap.String("remote_addr", r.RemoteAddr),
		)

		lrw := newLoggingResponseWriter(w, httpLogBodyLimit)
		next.ServeHTTP(lrw, r)

		status := lrw.Status()
		respBody, respTruncated := lrw.Body()
		logger.Debug("outgoing http response",
			zap.String("method", r.Method),
			zap.String("url", r.URL.String()),
			zap.Int("status", status),
			zap.String("body", respBody),
			zap.Bool("body_truncated", respTruncated),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Duration("cost", time.Since(startAt)),
		)
	})
}

func readAndRestoreRequestBody(r *http.Request, limit int) (string, bool, error) {
	if r.Body == nil {
		return "", false, nil
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return "", false, err
	}
	if err := r.Body.Close(); err != nil {
		return "", false, err
	}

	r.Body = io.NopCloser(bytes.NewReader(data))
	truncatedBody, truncated := truncateForLog(data, limit)
	return truncatedBody, truncated, nil
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status    int
	buffer    bytes.Buffer
	truncated bool
	bodyLimit int
}

func newLoggingResponseWriter(w http.ResponseWriter, limit int) *loggingResponseWriter {
	return &loggingResponseWriter{
		ResponseWriter: w,
		bodyLimit:      limit,
	}
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.status == 0 {
		lrw.status = http.StatusOK
	}

	if lrw.buffer.Len() < lrw.bodyLimit {
		remaining := lrw.bodyLimit - lrw.buffer.Len()
		if len(b) > remaining {
			lrw.buffer.Write(b[:remaining])
			lrw.truncated = true
		} else {
			lrw.buffer.Write(b)
		}
	} else {
		lrw.truncated = true
	}

	return lrw.ResponseWriter.Write(b)
}

func (lrw *loggingResponseWriter) Status() int {
	if lrw.status == 0 {
		return http.StatusOK
	}
	return lrw.status
}

func (lrw *loggingResponseWriter) Body() (string, bool) {
	return lrw.buffer.String(), lrw.truncated
}

func (lrw *loggingResponseWriter) Flush() {
	if flusher, ok := lrw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (lrw *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := lrw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("hijacker not supported")
}

func (lrw *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := lrw.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func truncateForLog(data []byte, limit int) (string, bool) {
	if len(data) <= limit {
		return string(data), false
	}
	return string(data[:limit]), true
}
