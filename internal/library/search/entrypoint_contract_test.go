package search

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

type contractProvider struct {
	query  string
	calls  int
	result *searchlib.SearchOutput
}

func (p *contractProvider) Search(_ context.Context, query string) (*searchlib.SearchOutput, error) {
	p.query = query
	p.calls++
	return p.result, nil
}
func contractContext(t *testing.T) context.Context {
	t.Helper()
	auth, err := mcpauth.ParseAuthorizationContext("Bearer alice:agent@sk-contract-test-only")
	require.NoError(t, err)
	return mcpauth.WithContext(context.Background(), auth)
}
func TestSearchGraphQLCanonicalIdentityAndNormalizedQuery(t *testing.T) {
	provider := &contractProvider{result: &searchlib.SearchOutput{EngineName: "stub"}}
	resolver := NewMutationResolver(provider, nil, nil)
	charges := 0
	resolver.billingChecker = func(_ context.Context, key string, _ oneapi.Price, scene string) error {
		charges++
		require.Equal(t, "sk-contract-test-only", key)
		require.Equal(t, "web_search", scene)
		return nil
	}
	result, err := resolver.WebSearch(contractContext(t), "  hello 漢  ")
	require.NoError(t, err)
	require.Equal(t, "hello 漢", provider.query)
	require.Equal(t, 1, charges)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, "stub", result.EngineName)
	require.NotNil(t, result.Results)
}
func TestGraphQLRejectsInvalidUnavailableAndUnauthenticatedBeforeBilling(t *testing.T) {
	provider := &contractProvider{result: &searchlib.SearchOutput{}}
	resolver := NewMutationResolver(provider, &rlibs.DB{}, nil)
	charges := 0
	resolver.billingChecker = func(context.Context, string, oneapi.Price, string) error { charges++; return nil }
	_, err := resolver.WebSearch(contractContext(t), " \t ")
	require.Error(t, err)
	_, err = resolver.WebSearch(context.Background(), "valid")
	require.Error(t, err)
	_, err = resolver.WebFetch(contractContext(t), "http://127.0.0.1/private", nil)
	require.Error(t, err)
	resolver.provider, resolver.rdb = nil, nil
	_, err = resolver.WebSearch(contractContext(t), "valid")
	require.Error(t, err)
	_, err = resolver.WebFetch(contractContext(t), "https://8.8.8.8/", nil)
	require.Error(t, err)
	require.Zero(t, charges)
	require.Zero(t, provider.calls)
}
func TestGraphQLFetchUsesNormalizedIdentityOnce(t *testing.T) {
	resolver := NewMutationResolver(nil, &rlibs.DB{}, nil)
	charges, calls := 0, 0
	resolver.billingChecker = func(_ context.Context, key string, _ oneapi.Price, scene string) error {
		charges++
		require.Equal(t, "sk-contract-test-only", key)
		require.Equal(t, "web_fetch", scene)
		return nil
	}
	resolver.fetcher = func(_ context.Context, _ *rlibs.DB, url, key string, markdown bool) ([]byte, error) {
		calls++
		require.Equal(t, "https://8.8.8.8/", url)
		require.Equal(t, "sk-contract-test-only", key)
		require.True(t, markdown)
		return []byte("hello"), nil
	}
	result, err := resolver.WebFetch(contractContext(t), "  https://8.8.8.8/  ", nil)
	require.NoError(t, err)
	require.Equal(t, "hello", result.Content)
	require.Equal(t, 1, charges)
	require.Equal(t, 1, calls)
}
func TestGraphQLNilProviderOutputIsAnErrorNotAPanic(t *testing.T) {
	resolver := NewMutationResolver(&contractProvider{}, nil, nil)
	resolver.billingChecker = func(context.Context, string, oneapi.Price, string) error { return nil }
	_, err := resolver.WebSearch(contractContext(t), "hello")
	require.ErrorContains(t, err, "no result")
}
