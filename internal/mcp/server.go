package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

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
	handler      http.Handler
	logger       logSDK.Logger
	searchEngine *bing.SearchEngine
}

// NewServer constructs a remote MCP server exposing HTTP endpoints under a single handler.
func NewServer(searchEngine *bing.SearchEngine, logger logSDK.Logger) (*Server, error) {
	if searchEngine == nil {
		return nil, fmt.Errorf("search engine is required")
	}
	if logger == nil {
		logger = log.Logger
	}

	mcpServer := srv.NewMCPServer(
		"laisky-blog-graphql",
		"1.0.0",
		srv.WithToolCapabilities(true),
		srv.WithInstructions("Use the web_search tool to run Bing-powered web searches."),
		srv.WithRecovery(),
	)

	streamable := srv.NewStreamableHTTPServer(
		mcpServer,
		srv.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, keyAuthorization, r.Header.Get("Authorization"))
		}),
	)

	s := &Server{
		handler:      streamable,
		logger:       logger.Named("mcp"),
		searchEngine: searchEngine,
	}

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

	return s, nil
}

// Handler returns the HTTP handler that should be mounted to serve MCP traffic.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) handleWebSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
