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
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

func TestWebSearchHandleMissingAPIKey(t *testing.T) {
	tool := mustWebSearchTool(t,
		func(context.Context) string { return "" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		&stubSearchEngine{},
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
		&stubSearchEngine{},
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
	engine := &stubSearchEngine{err: expectedErr}
	var billingCalls int

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "token" },
		func(context.Context, string, oneapi.Price, string) error {
			billingCalls++
			return nil
		},
		engine,
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
	engine := &stubSearchEngine{
		result: &google.CustomSearchResponse{
			Items: []google.SearchResultItem{
				{
					Link:    "https://example.com",
					Title:   "Example",
					Snippet: "Snippet",
				},
			},
		},
	}

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "token" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		engine,
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

	var payload search.SearchResult
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, "golang", payload.Query)
	require.True(t, payload.CreatedAt.Equal(fixedTime))
	require.Len(t, payload.Results, 1)
	require.Equal(t, "https://example.com", payload.Results[0].URL)
	require.Equal(t, "Example", payload.Results[0].Name)
	require.Equal(t, "Snippet", payload.Results[0].Snippet)
}

type stubSearchEngine struct {
	result *google.CustomSearchResponse
	err    error
}

func (s *stubSearchEngine) Search(context.Context, string) (*google.CustomSearchResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func mustWebSearchTool(t *testing.T, keyProvider APIKeyProvider, billing BillingChecker, engine SearchEngine, clock Clock) *WebSearchTool {
	t.Helper()

	tool, err := NewWebSearchTool(engine, log.Logger.Named("test_web_search"), keyProvider, billing, clock)
	require.NoError(t, err)
	return tool
}
