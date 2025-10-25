package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

func TestWebSearchHandleMissingAPIKey(t *testing.T) {
	tool := mustWebSearchTool(t,
		func(context.Context) string { return "" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		&stubSearchProvider{},
		fixedClock(time.Unix(0, 0)),
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "golang",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "missing authorization bearer token", textContent.Text)
}

func TestWebSearchHandleBillingError(t *testing.T) {
	expectedErr := errors.New("quota exceeded")
	var billingCalls int

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "token" },
		func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error {
			billingCalls++
			require.Equal(t, oneapi.PriceWebSearch, price)
			require.Equal(t, "web search", reason)
			return expectedErr
		},
		&stubSearchProvider{},
		fixedClock(time.Unix(0, 0)),
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "golang",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, billingCalls)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "billing check failed: quota exceeded")
}

func TestWebSearchHandleSearchError(t *testing.T) {
	expectedErr := errors.New("search backend down")
	provider := &stubSearchProvider{err: expectedErr}
	var billingCalls int

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "token" },
		func(context.Context, string, oneapi.Price, string) error {
			billingCalls++
			return nil
		},
		provider,
		fixedClock(time.Unix(0, 0)),
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "golang",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, billingCalls)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "search failed: search backend down")
}

func TestWebSearchHandleSuccess(t *testing.T) {
	fixedTime := time.Date(2025, time.October, 25, 12, 0, 0, 0, time.UTC)
	provider := &stubSearchProvider{
		items: []searchlib.SearchResultItem{
			{
				URL:     "https://example.com",
				Name:    "Example",
				Snippet: "Snippet",
			},
		},
	}

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "token" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		provider,
		fixedClock(fixedTime),
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"query": "golang",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	var payload searchlib.SearchResult
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, "golang", payload.Query)
	require.True(t, payload.CreatedAt.Equal(fixedTime))
	require.Len(t, payload.Results, 1)
	require.Equal(t, "https://example.com", payload.Results[0].URL)
	require.Equal(t, "Example", payload.Results[0].Name)
	require.Equal(t, "Snippet", payload.Results[0].Snippet)
}

type stubSearchProvider struct {
	items []searchlib.SearchResultItem
	err   error
}

func (s *stubSearchProvider) Search(context.Context, string) ([]searchlib.SearchResultItem, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

func mustWebSearchTool(t *testing.T, keyProvider APIKeyProvider, billing BillingChecker, provider SearchProvider, clock Clock) *WebSearchTool {
	t.Helper()

	tool, err := NewWebSearchTool(provider, log.Logger.Named("test_web_search"), keyProvider, billing, clock)
	require.NoError(t, err)
	return tool
}
