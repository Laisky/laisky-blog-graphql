package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/bing"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
)

type ctxKey string

const (
	keyAuthorization ctxKey = "authorization"
)

// Server wraps the MCP server state for the HTTP transport.
type Server struct {
	handler        http.Handler
	logger         logSDK.Logger
	searchEngine   *bing.SearchEngine
	askUserService *askuser.Service
}

// NewServer constructs a remote MCP server exposing HTTP endpoints under a single handler.
func NewServer(searchEngine *bing.SearchEngine, askUserService *askuser.Service, logger logSDK.Logger) (*Server, error) {
	if searchEngine == nil && askUserService == nil {
		return nil, fmt.Errorf("at least one MCP capability must be enabled")
	}
	if logger == nil {
		logger = log.Logger
	}

	hooks := newMCPHooks(logger.Named("mcp_hooks"))

	mcpServer := srv.NewMCPServer(
		"laisky-blog-graphql",
		"1.0.0",
		srv.WithToolCapabilities(true),
		srv.WithInstructions("Use the web_search tool to run Bing-powered web searches."),
		srv.WithRecovery(),
		srv.WithHooks(hooks),
	)

	streamable := srv.NewStreamableHTTPServer(
		mcpServer,
		srv.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, keyAuthorization, r.Header.Get("Authorization"))
		}),
	)

	s := &Server{
		handler:        streamable,
		logger:         logger.Named("mcp"),
		searchEngine:   searchEngine,
		askUserService: askUserService,
	}

	if searchEngine != nil {
		tool := mcp.NewTool(
			"web_search",
			mcp.WithDescription("Search the public web using Bing and return a structured result set."),
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
		for _, item := range result.WebPages.Value {
			response.Results = append(response.Results, search.SearchResultItem{
				URL:     item.URL,
				Name:    item.Name,
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

func extractAPIKey(authHeader string) string {
	if authHeader == "" {
		return ""
	}

	value := strings.TrimSpace(authHeader)
	const prefix = "Bearer "
	if strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
		return strings.TrimSpace(value[len(prefix):])
	}

	return value
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
