package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
)

type ctxKey string

const (
	keyAuthorization ctxKey = "authorization"
	httpLogBodyLimit        = 4096
)

// Server wraps the MCP server state for the HTTP transport.
type Server struct {
	handler        http.Handler
	logger         logSDK.Logger
	searchEngine   *google.SearchEngine
	rdb            *rlibs.DB
	askUserService *askuser.Service
}

// NewServer constructs an MCP HTTP server.
// searchEngine enables the web_search tool when not nil.
// askUserService enables the ask_user tool when not nil.
// rdb enables the web_fetch tool when not nil.
// logger overrides the default logger when provided.
// It returns the configured server or an error if no capability is available.
func NewServer(searchEngine *google.SearchEngine, askUserService *askuser.Service, rdb *rlibs.DB, logger logSDK.Logger) (*Server, error) {
	if searchEngine == nil && askUserService == nil && rdb == nil {
		return nil, fmt.Errorf("at least one MCP capability must be enabled")
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

	streamable := srv.NewStreamableHTTPServer(
		mcpServer,
		srv.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, keyAuthorization, r.Header.Get("Authorization"))
		}),
	)

	serverLogger := logger.Named("mcp")

	s := &Server{
		handler:        withHTTPLogging(streamable, serverLogger.Named("http")),
		logger:         serverLogger,
		searchEngine:   searchEngine,
		rdb:            rdb,
		askUserService: askUserService,
	}

	if searchEngine != nil {
		tool := mcp.NewTool(
			"web_search",
			mcp.WithDescription("Search the public web using Google Programmable Search and return a structured result set."),
			mcp.WithString(
				"query",
				mcp.Required(),
				mcp.Description("Plain text search query."),
			),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(true),
		)

		mcpServer.AddTool(tool, s.handleWebSearch)
	}

	if rdb != nil {
		fetchTool := mcp.NewTool(
			"web_fetch",
			mcp.WithDescription("Fetch and render dynamic web content by URL."),
			mcp.WithString(
				"url",
				mcp.Required(),
				mcp.Description("The URL to retrieve."),
			),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(true),
		)

		mcpServer.AddTool(fetchTool, s.handleWebFetch)
	}

	if askUserService != nil {
		askTool := mcp.NewTool(
			"ask_user",
			mcp.WithDescription("Forward a question to the authenticated user and wait for a response."),
			mcp.WithString(
				"question",
				mcp.Required(),
				mcp.Description("The question that should be surfaced to the user."),
			),
			mcp.WithIdempotentHintAnnotation(false),
		)

		mcpServer.AddTool(askTool, s.handleAskUser)
	}

	return s, nil
}

// Handler returns the HTTP handler that should be mounted to serve MCP traffic.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) handleWebSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.searchEngine == nil {
		return mcp.NewToolResultError("web search is not configured"), nil
	}

	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return mcp.NewToolResultError("query cannot be empty"), nil
	}

	authHeader, _ := ctx.Value(keyAuthorization).(string)
	apiKey := extractAPIKey(authHeader)
	if apiKey == "" {
		s.logger.Warn("web_search missing api key", zap.String("query", query))
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	if err := oneapi.CheckUserExternalBilling(ctx, apiKey, oneapi.PriceWebSearch, "web search"); err != nil {
		s.logger.Warn("web_search billing denied", zap.Error(err), zap.String("query", query))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	result, err := s.searchEngine.Search(ctx, query)
	if err != nil {
		s.logger.Error("web_search failed", zap.Error(err), zap.String("query", query))
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	response := search.SearchResult{
		Query:     query,
		CreatedAt: time.Now().UTC(),
	}

	if result != nil {
		for _, item := range result.Items {
			response.Results = append(response.Results, search.SearchResultItem{
				URL:     item.Link,
				Name:    item.Title,
				Snippet: item.Snippet,
			})
		}
	}

	toolResult, err := mcp.NewToolResultJSON(response)
	if err != nil {
		s.logger.Error("encode search result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode search result"), nil
	}

	return toolResult, nil
}

// handleWebFetch executes the web_fetch MCP tool. The context carries request metadata,
// and the request supplies the target URL. It returns a structured response when the
// fetch succeeds or a tool error when processing fails.
func (s *Server) handleWebFetch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultError("web fetch is not configured"), nil
	}

	urlValue, err := req.RequireString("url")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	urlValue = strings.TrimSpace(urlValue)
	if urlValue == "" {
		return mcp.NewToolResultError("url cannot be empty"), nil
	}

	authHeader, _ := ctx.Value(keyAuthorization).(string)
	apiKey := extractAPIKey(authHeader)
	if apiKey == "" {
		s.logger.Warn("web_fetch missing api key", zap.String("url", urlValue))
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	if err := oneapi.CheckUserExternalBilling(ctx, apiKey, oneapi.PriceWebFetch, "web fetch"); err != nil {
		s.logger.Warn("web_fetch billing denied", zap.Error(err), zap.String("url", urlValue))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	content, err := search.FetchDynamicURLContent(ctx, s.rdb, urlValue)
	if err != nil {
		s.logger.Error("web_fetch failed", zap.Error(err), zap.String("url", urlValue))
		return mcp.NewToolResultError(fmt.Sprintf("fetch failed: %v", err)), nil
	}

	payload := map[string]any{
		"url":        urlValue,
		"content":    string(content),
		"fetched_at": time.Now().UTC(),
	}

	toolResult, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		s.logger.Error("encode web_fetch result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode web_fetch response"), nil
	}

	return toolResult, nil
}

func extractAPIKey(authHeader string) string {
	return library.StripBearerPrefix(authHeader)
}

func (s *Server) handleAskUser(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.askUserService == nil {
		return mcp.NewToolResultError("ask_user tool is not available"), nil
	}

	question, err := req.RequireString("question")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return mcp.NewToolResultError("question cannot be empty"), nil
	}

	authHeader, _ := ctx.Value(keyAuthorization).(string)
	authCtx, err := askuser.ParseAuthorizationContext(authHeader)
	if err != nil {
		s.logger.Warn("ask_user authorization failed", zap.Error(err))
		return mcp.NewToolResultError("invalid authorization header"), nil
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	stored, err := s.askUserService.CreateRequest(callCtx, authCtx, question)
	if err != nil {
		s.logger.Error("ask_user create request", zap.Error(err))
		return mcp.NewToolResultError("failed to create ask_user request"), nil
	}

	answered, err := s.askUserService.WaitForAnswer(callCtx, stored.ID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			_ = s.askUserService.CancelRequest(context.Background(), stored.ID, askuser.StatusExpired)
			return mcp.NewToolResultError("timeout waiting for user response"), nil
		}
		s.logger.Error("ask_user wait for answer", zap.Error(err))
		return mcp.NewToolResultError("failed while waiting for user response"), nil
	}

	if answered.Answer == nil {
		return mcp.NewToolResultError("user responded without an answer"), nil
	}

	resultPayload := map[string]any{
		"request_id": answered.ID.String(),
		"question":   answered.Question,
		"answer":     *answered.Answer,
		"asked_at":   answered.CreatedAt,
	}
	if answered.AnsweredAt != nil {
		resultPayload["answered_at"] = answered.AnsweredAt
	}

	toolResult, err := mcp.NewToolResultJSON(resultPayload)
	if err != nil {
		s.logger.Error("encode ask_user response", zap.Error(err))
		return mcp.NewToolResultError("failed to encode ask_user response"), nil
	}

	return toolResult, nil
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
