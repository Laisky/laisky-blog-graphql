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

func TestWebFetchHandleOutputMarkdownExplicitFalse(t *testing.T) {
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
				"output_markdown": false,
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.False(t, gotOutputMarkdown)
}

func TestWebFetchHandleOutputMarkdownInvalidStringDefaultsTrue(t *testing.T) {
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
				"output_markdown": "unexpected-value",
			},
		},
	}

	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.True(t, gotOutputMarkdown)
}

func TestResolveOutputMarkdownArg(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		name     string
		args     any
		expected bool
	}{
		{name: "missing arguments defaults true", args: nil, expected: true},
		{name: "missing field defaults true", args: map[string]any{"url": "https://example.com"}, expected: true},
		{name: "bool false respected", args: map[string]any{"output_markdown": false}, expected: false},
		{name: "string false respected", args: map[string]any{"output_markdown": "false"}, expected: false},
		{name: "zero respected", args: map[string]any{"output_markdown": 0}, expected: false},
		{name: "garbage defaults true", args: map[string]any{"output_markdown": []string{"nope"}}, expected: true},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.expected, resolveOutputMarkdownArg(tc.args))
		})
	}
}

func TestValidateFetchURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "valid https", url: "https://example.com", wantErr: false},
		{name: "valid http", url: "http://example.com/page", wantErr: false},
		{name: "blocked file scheme", url: "file:///etc/passwd", wantErr: true},
		{name: "blocked ftp scheme", url: "ftp://example.com/file", wantErr: true},
		{name: "blocked gopher scheme", url: "gopher://evil.com", wantErr: true},
		{name: "blocked data scheme", url: "data:text/html,<h1>hi</h1>", wantErr: true},
		{name: "empty scheme", url: "://example.com", wantErr: true},
		{name: "loopback localhost", url: "http://localhost/admin", wantErr: true},
		{name: "loopback 127.0.0.1", url: "http://127.0.0.1/admin", wantErr: true},
		{name: "private 10.x", url: "http://10.0.0.1/internal", wantErr: true},
		{name: "private 172.16.x", url: "http://172.16.0.1/internal", wantErr: true},
		{name: "private 192.168.x", url: "http://192.168.1.1/internal", wantErr: true},
		{name: "link-local", url: "http://169.254.169.254/metadata", wantErr: true},
		{name: "unspecified 0.0.0.0", url: "http://0.0.0.0/", wantErr: true},
		{name: "ipv6 loopback", url: "http://[::1]/admin", wantErr: true},
		{name: "no hostname", url: "http:///path", wantErr: true},
		{name: "unresolvable host", url: "http://this-host-does-not-exist-12345.invalid/", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFetchURL(tc.url)
			if tc.wantErr {
				require.Error(t, err, "expected error for url %q", tc.url)
			} else {
				require.NoError(t, err, "unexpected error for url %q", tc.url)
			}
		})
	}
}

func TestWebFetchHandleBlocksSSRF(t *testing.T) {
	tool := mustWebFetchTool(t,
		func(context.Context) string { return "token" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			t.Fatal("fetcher should not be called for blocked URLs")
			return nil, nil
		},
	)

	ssrfURLs := []string{
		"file:///etc/passwd",
		"http://127.0.0.1/admin",
		"http://localhost/secret",
		"http://169.254.169.254/latest/meta-data/",
	}

	for _, u := range ssrfURLs {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"url": u},
			},
		}

		result, err := tool.Handle(context.Background(), req)
		require.NoError(t, err)
		require.True(t, result.IsError, "expected error result for SSRF url %q", u)

		textContent, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok)
		require.Contains(t, textContent.Text, "invalid url")
	}
}

func mustWebFetchTool(t *testing.T, keyProvider APIKeyProvider, billing BillingChecker, fetcher DynamicFetcher) *WebFetchTool {
	t.Helper()

	tool, err := NewWebFetchTool(&rlibs.DB{}, log.Logger.Named("test_web_fetch"), keyProvider, billing, fetcher)
	require.NoError(t, err)
	return tool
}
