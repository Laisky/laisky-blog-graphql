package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// SearchEngine defines the subset of methods used from the Google search client.
type SearchEngine interface {
	Search(context.Context, string) (*google.CustomSearchResponse, error)
}

// BillingChecker validates external billing quotas for tool usage requests.
type BillingChecker func(context.Context, string, oneapi.Price, string) error

// WebSearchTool implements the web_search MCP tool.
type WebSearchTool struct {
	searchEngine   SearchEngine
	logger         logSDK.Logger
	apiKeyProvider APIKeyProvider
	billingChecker BillingChecker
	clock          Clock
}

// NewWebSearchTool constructs a WebSearchTool with the provided dependencies.
func NewWebSearchTool(engine SearchEngine, logger logSDK.Logger, apiKeyProvider APIKeyProvider, billingChecker BillingChecker, clock Clock) (*WebSearchTool, error) {
	if engine == nil {
		return nil, errors.New("search engine is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if apiKeyProvider == nil {
		return nil, errors.New("api key provider is required")
	}
	if billingChecker == nil {
		return nil, errors.New("billing checker is required")
	}
	if clock == nil {
		clock = func() time.Time {
			return time.Now().UTC()
		}
	}

	return &WebSearchTool{
		searchEngine:   engine,
		logger:         logger,
		apiKeyProvider: apiKeyProvider,
		billingChecker: billingChecker,
		clock:          clock,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *WebSearchTool) Definition() mcp.Tool {
	return mcp.NewTool(
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
}

// Handle executes the web_search tool logic using the configured dependencies.
func (t *WebSearchTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return mcp.NewToolResultError("query cannot be empty"), nil
	}

	apiKey := t.apiKeyProvider(ctx)
	if apiKey == "" {
		t.logger.Warn("web_search missing api key", zap.String("query", query))
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	if err := t.billingChecker(ctx, apiKey, oneapi.PriceWebSearch, "web search"); err != nil {
		t.logger.Warn("web_search billing denied", zap.Error(err), zap.String("query", query))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	result, err := t.searchEngine.Search(ctx, query)
	if err != nil {
		t.logger.Error("web_search failed", zap.Error(err), zap.String("query", query))
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	response := search.SearchResult{
		Query:     query,
		CreatedAt: t.clock(),
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
		t.logger.Error("encode search result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode search result"), nil
	}

	return toolResult, nil
}
