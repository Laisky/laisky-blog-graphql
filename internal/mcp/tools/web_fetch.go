package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// DynamicFetcher retrieves rendered HTML content for a given URL.
type DynamicFetcher func(ctx context.Context, store *rlibs.DB, url string, apiKey string, outputMarkdown bool) ([]byte, error)

// WebFetchTool implements the web_fetch MCP tool.
type WebFetchTool struct {
	store          *rlibs.DB
	logger         logSDK.Logger
	apiKeyProvider APIKeyProvider
	billingChecker BillingChecker
	fetcher        DynamicFetcher
}

// NewWebFetchTool constructs a WebFetchTool with the provided dependencies.
func NewWebFetchTool(store *rlibs.DB, logger logSDK.Logger, apiKeyProvider APIKeyProvider, billingChecker BillingChecker, fetcher DynamicFetcher) (*WebFetchTool, error) {
	if store == nil {
		return nil, errors.New("redis client is required")
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
	if fetcher == nil {
		return nil, errors.New("dynamic fetcher is required")
	}

	return &WebFetchTool{
		store:          store,
		logger:         logger,
		apiKeyProvider: apiKeyProvider,
		billingChecker: billingChecker,
		fetcher:        fetcher,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *WebFetchTool) Definition() mcp.Tool {
	return mcp.NewTool(
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
}

// Handle executes the web_fetch tool logic using the configured dependencies.
func (t *WebFetchTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urlValue, err := req.RequireString("url")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	urlValue = strings.TrimSpace(urlValue)
	if urlValue == "" {
		return mcp.NewToolResultError("url cannot be empty"), nil
	}

	apiKey := t.apiKeyProvider(ctx)
	if apiKey == "" {
		t.logger.Warn("web_fetch missing api key", zap.String("url", urlValue))
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	outputMarkdown := true // default to convert to markdown
	if args, ok := req.Params.Arguments.(map[string]any); ok {
		if raw, ok := args["output_markdown"]; ok {
			outputMarkdown = parseOptionalBool(raw)
		}
	}

	if err := t.billingChecker(ctx, apiKey, oneapi.PriceWebFetch, "web fetch"); err != nil {
		t.logger.Warn("web_fetch billing denied", zap.Error(err), zap.String("url", urlValue))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	content, err := t.fetcher(ctx, t.store, urlValue, apiKey, outputMarkdown)
	if err != nil {
		t.logger.Error("web_fetch failed", zap.Error(err), zap.String("url", urlValue))
		return mcp.NewToolResultError(fmt.Sprintf("fetch failed: %v", err)), nil
	}

	payload := map[string]any{
		"content": string(content),
	}

	toolResult, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		t.logger.Error("encode web_fetch result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode web_fetch response"), nil
	}

	return toolResult, nil
}

func parseOptionalBool(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		return s == "true" || s == "1" || s == "yes" || s == "y" || s == "on"
	case float64:
		// MCP JSON numbers decode into float64
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case uint64:
		return v != 0
	default:
		return false
	}
}
