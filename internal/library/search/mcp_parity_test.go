package search

import (
	"context"
	"strings"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// These tests exercise real transport adapters, with only paid/provider effects
// substituted. They do not stand in for a generated GraphQL HTTP transport test.
func TestSearchAdmissionAcrossMCPAndGraphQL(t *testing.T) {
	for _, query := range []string{" \t\n ", string([]byte{0xff}), strings.Repeat("x", toolpolicy.MaxQueryBytes+1)} {
		provider := &contractProvider{result: &searchlib.SearchOutput{}}
		resolver := NewMutationResolver(provider, nil, nil)
		charges := 0
		billing := func(context.Context, string, oneapi.Price, string) error { charges++; return nil }
		resolver.billingChecker = billing
		ctx := contractContext(t)
		tool, err := tools.NewWebSearchTool(provider, log.Logger, func(ctx context.Context) string {
			auth, _ := mcpauth.FromContext(ctx)
			return auth.APIKey
		}, billing)
		require.NoError(t, err)
		_, err = resolver.WebSearch(ctx, query)
		require.Error(t, err)
		result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": query}}})
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Zero(t, charges)
		require.Zero(t, provider.calls)
	}
}

func TestSearchSuccessHasOneChargePerEntryPoint(t *testing.T) {
	provider := &contractProvider{result: &searchlib.SearchOutput{}}
	resolver := NewMutationResolver(provider, nil, nil)
	charges := 0
	billing := func(_ context.Context, key string, price oneapi.Price, scene string) error {
		charges++
		require.Equal(t, "sk-contract-test-only", key)
		require.Equal(t, oneapi.PriceWebSearch, price)
		require.Equal(t, "web_search", scene)
		return nil
	}
	resolver.billingChecker = billing
	tool, err := tools.NewWebSearchTool(provider, log.Logger, func(context.Context) string { return "sk-contract-test-only" }, billing)
	require.NoError(t, err)
	ctx := contractContext(t)
	_, err = resolver.WebSearch(ctx, "  shared query  ")
	require.NoError(t, err)
	result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "  shared query  "}}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "shared query", provider.query)
	require.Equal(t, 2, charges)
	require.Equal(t, 2, provider.calls)
}

func TestFetchAdmissionAcrossMCPAndGraphQL(t *testing.T) {
	for _, url := range []string{"file:///etc/passwd", "http://127.0.0.1", "http://169.254.169.254", "https://user:secret@8.8.8.8/", "http://2130706433"} {
		rdb := &rlibs.DB{}
		resolver := NewMutationResolver(nil, rdb, nil)
		charges, calls := 0, 0
		billing := func(context.Context, string, oneapi.Price, string) error { charges++; return nil }
		fetcher := func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) {
			calls++
			return []byte("unused"), nil
		}
		resolver.billingChecker, resolver.fetcher = billing, fetcher
		tool, err := tools.NewWebFetchTool(rdb, log.Logger, func(context.Context) string { return "sk-contract-test-only" }, billing, fetcher)
		require.NoError(t, err)
		ctx := contractContext(t)
		_, err = resolver.WebFetch(ctx, url)
		require.Error(t, err)
		result, err := tool.Handle(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"url": url}}})
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Zero(t, charges)
		require.Zero(t, calls)
	}
}
