package tools

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

// SearchProvider abstracts the search execution capability used by the tool.
// The Search method accepts a context and plain-text query string, returning
// search output with items and engine metadata, or an error when the lookup fails.
type SearchProvider interface {
	Search(context.Context, string) (*searchlib.SearchOutput, error)
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
		mcp.WithDescription("Search the public web using search engines (Google, Bing) and return a structured result set. Use this to find information, articles, documentation, or answers from the internet."),
		mcp.WithString(
			"query",
			mcp.Required(),
			mcp.Description("Plain text search query; trimmed, non-empty, at most 16384 UTF-8 bytes."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
}

// Handle executes the web_search tool logic using the configured dependencies.
func (t *WebSearchTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logger := webSearchLogger(t.logger)
	requestID, _ := ctx.Value(ctxkeys.RequestID).(string)
	parsedID, idErr := uuid.Parse(requestID)
	if idErr != nil || len(requestID) != 36 || parsedID == uuid.Nil || parsedID.String() != requestID {
		requestID = uuid.NewString()
	}
	ctx = diagnostics.WithRequestID(ctx, requestID)
	logger = logger.With(zap.String("request_id", requestID))
	ctx = diagnostics.WithLogger(ctx, logger)
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	query, err = toolpolicy.Query(query)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	start := time.Now().UTC()
	logger.Debug("web_search started")

	apiKey := t.apiKeyProvider(ctx)
	if apiKey == "" {
		logger.Warn("web_search missing api key")
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	logger.Debug("web_search billing check started")

	if err := t.billingChecker(ctx, apiKey, oneapi.PriceWebSearch, "web_search"); err != nil {
		logger.Warn("web_search billing denied", zap.Error(toolBillingLogError(err)))
		return mcp.NewToolResultError("billing check failed"), nil
	}

	logger.Debug("web_search billing check passed")

	output, err := t.searchProvider.Search(ctx, query)
	if err != nil {
		diagnostic := diagnostics.Summary(err)
		fields := appLog.WebSearchDiagnosticFields(appLog.WebSearchFailureSummary{Diagnostic: diagnostic, DurationMS: time.Since(start).Milliseconds()})
		logger.Error("web_search failed", append(fields, zap.Error(diagnostics.LogError(err)))...)
		return mcp.NewToolResultError("search failed: " + diagnostic.Category), nil
	}

	if output == nil {
		fields := appLog.WebSearchDiagnosticFields(appLog.WebSearchFailureSummary{Diagnostic: diagnostics.Summary(diagnostics.New("provider", "internal")), DurationMS: time.Since(start).Milliseconds()})
		logger.Error("web_search failed", fields...)
		return mcp.NewToolResultError("search provider returned no result"), nil
	}

	logger.Debug("web_search completed",
		zap.Int("results_count", len(output.Items)),
		zap.String("provider", diagnostics.Normalize(diagnostics.Diagnostic{Provider: output.EngineType}).Provider),
		zap.Duration("duration", time.Since(start)),
	)

	response := searchlib.SimplifiedSearchResult{
		Results: append([]searchlib.SearchResultItem{}, output.Items...),
	}

	toolResult, err := mcp.NewToolResultJSON(response)
	if err != nil {
		fields := appLog.WebSearchDiagnosticFields(appLog.WebSearchFailureSummary{Diagnostic: diagnostics.Summary(diagnostics.New("encode", "internal")), DurationMS: time.Since(start).Milliseconds()})
		logger.Error("web_search failed", fields...)
		return mcp.NewToolResultError("failed to encode search result"), nil //nolint:nilerr // MCP execution errors are tool results, not transport errors.
	}

	return toolResult, nil
}

func webSearchLogger(fallback logSDK.Logger) logSDK.Logger {
	// The MCP framework logger carries client addresses and paths. Start from
	// the injected application base and attach only the reviewed correlation ID.
	logger := fallback
	if named, ok := logger.(interface{ Name() string }); ok && strings.HasSuffix(named.Name(), ".web_search") {
		return logger
	}
	return logger.Named("web_search")
}
