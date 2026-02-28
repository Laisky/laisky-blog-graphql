package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// SearchProvider abstracts the search execution capability used by the tool.
// The Search method accepts a context and plain-text query string, returning
// zero or more search items or an error when the lookup fails.
type SearchProvider interface {
	Search(context.Context, string) ([]searchlib.SearchResultItem, error)
}

// BillingChecker validates external billing quotas for tool usage requests.
type BillingChecker func(context.Context, string, oneapi.Price, string) error

// WebSearchTool implements the web_search MCP tool.
type WebSearchTool struct {
	searchProvider SearchProvider
	logger         logSDK.Logger
	apiKeyProvider APIKeyProvider
	billingChecker BillingChecker
}

// NewWebSearchTool constructs a WebSearchTool with the provided dependencies.
func NewWebSearchTool(provider SearchProvider, logger logSDK.Logger, apiKeyProvider APIKeyProvider, billingChecker BillingChecker) (*WebSearchTool, error) {
	if provider == nil {
		return nil, errors.New("search provider is required")
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

	return &WebSearchTool{
		searchProvider: provider,
		logger:         logger,
		apiKeyProvider: apiKeyProvider,
		billingChecker: billingChecker,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *WebSearchTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"web_search",
		mcp.WithDescription("Search the public web using search engines and return a structured result set."),
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

	start := time.Now().UTC()
	t.logger.Debug("web_search started", zap.Int("query_len", len(query)))

	apiKey := t.apiKeyProvider(ctx)
	if apiKey == "" {
		t.logger.Warn("web_search missing api key", zap.Int("query_len", len(query)))
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	t.logger.Debug("web_search billing check started", zap.Int("query_len", len(query)))

	if err := t.billingChecker(ctx, apiKey, oneapi.PriceWebSearch, "web search"); err != nil {
		t.logger.Warn("web_search billing denied", zap.Error(err), zap.Int("query_len", len(query)))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	t.logger.Debug("web_search billing check passed", zap.Int("query_len", len(query)))

	items, err := t.searchProvider.Search(ctx, query)
	if err != nil {
		t.logger.Error("web_search failed", zap.Error(err), zap.Int("query_len", len(query)))
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	t.logger.Debug("web_search completed",
		zap.Int("query_len", len(query)),
		zap.Int("results_count", len(items)),
		zap.Duration("duration", time.Since(start)),
	)

	response := searchlib.SimplifiedSearchResult{
		Results: items,
	}

	toolResult, err := mcp.NewToolResultJSON(response)
	if err != nil {
		t.logger.Error("encode search result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode search result"), nil
	}

	return toolResult, nil
}
