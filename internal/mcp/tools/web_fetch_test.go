package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Laisky/errors/v2"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

func TestWebFetchHandleMissingAPIKey(t *testing.T) {
	tool := mustWebFetchTool(t,
		func(context.Context) string { return "" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) { return []byte("ignored"), nil },
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"url": "https://example.com",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "missing authorization bearer token", textContent.Text)
}

func TestWebFetchHandleBillingError(t *testing.T) {
	expectedErr := errors.New("quota depleted")
	billingCalled := false

	tool := mustWebFetchTool(t,
		func(context.Context) string { return "token" },
		func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error {
			billingCalled = true
			require.Equal(t, "token", apiKey)
			require.Equal(t, oneapi.PriceWebFetch, price)
			require.Equal(t, "web fetch", reason)
			return expectedErr
		},
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) { return []byte("ignored"), nil },
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"url": "https://example.com",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, billingCalled)
	require.NotNil(t, result)
	require.True(t, result.IsError)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, textContent.Text, "billing check failed: quota depleted")
}

func TestWebFetchHandleSuccess(t *testing.T) {
	var billingCalls int
	var fetchCalls int

	tool := mustWebFetchTool(t,
		func(context.Context) string { return "token" },
		func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error {
			billingCalls++
			return nil
		},
		func(ctx context.Context, store *rlibs.DB, url string, apiKey string, outputMarkdown bool) ([]byte, error) {
			fetchCalls++
			require.Equal(t, "https://example.com", url)
			require.Equal(t, "token", apiKey)
			require.True(t, outputMarkdown)
			return []byte("<html>ok</html>"), nil
		},
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"url": "https://example.com",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Equal(t, 1, billingCalls)
	require.Equal(t, 1, fetchCalls)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	payload := make(map[string]any)
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &payload))
	require.Equal(t, "<html>ok</html>", payload["content"])
	require.NotContains(t, payload, "url")
	require.NotContains(t, payload, "fetched_at")
}

func TestWebFetchHandleOutputMarkdown(t *testing.T) {
	var gotOutputMarkdown bool

	tool := mustWebFetchTool(t,
		func(context.Context) string { return "token" },
		func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error { return nil },
		func(ctx context.Context, store *rlibs.DB, url string, apiKey string, outputMarkdown bool) ([]byte, error) {
			gotOutputMarkdown = outputMarkdown
			return []byte("ok"), nil
		},
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"url":             "https://example.com",
				"output_markdown": true,
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.True(t, gotOutputMarkdown)
}

func TestWebFetchHandleOutputMarkdownString(t *testing.T) {
	var gotOutputMarkdown bool

	tool := mustWebFetchTool(t,
		func(context.Context) string { return "token" },
		func(ctx context.Context, apiKey string, price oneapi.Price, reason string) error { return nil },
		func(ctx context.Context, store *rlibs.DB, url string, apiKey string, outputMarkdown bool) ([]byte, error) {
			gotOutputMarkdown = outputMarkdown
			return []byte("ok"), nil
		},
	)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"url":             "https://example.com",
				"output_markdown": "true",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.True(t, gotOutputMarkdown)
}

func mustWebFetchTool(t *testing.T, keyProvider APIKeyProvider, billing BillingChecker, fetcher DynamicFetcher) *WebFetchTool {
	t.Helper()

	tool, err := NewWebFetchTool(&rlibs.DB{}, log.Logger.Named("test_web_fetch"), keyProvider, billing, fetcher)
	require.NoError(t, err)
	return tool
}
