package web

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

type interfaceIsolationSearchProvider struct{ t *testing.T }

func (p interfaceIsolationSearchProvider) Search(context.Context, string) (*searchlib.SearchOutput, error) {
	p.t.Fatal("invalid input must not reach a provider")
	return nil, nil
}

// TestGraphQLFieldsIgnoreMCPRegistrationFlags exercises the production resolver
// builder with either MCP switch state. Inputs fail shared validation before
// billing, DNS or backend effects, not because the peer interface is disabled.
func TestGraphQLFieldsIgnoreMCPRegistrationFlags(t *testing.T) {
	auth, err := mcpauth.DeriveFromAPIKey("sk-interface-isolation-test-only")
	require.NoError(t, err)
	ctx := mcpauth.WithContext(context.Background(), auth)
	for _, enabled := range []bool{false, true} {
		args := ResolverArgs{
			WebSearchProvider: interfaceIsolationSearchProvider{t: t},
			Rdb:               &rlibs.DB{},
			RAGService:        &rag.Service{},
			RAGSettings:       rag.Settings{TopKDefault: 5, TopKLimit: 20, MaxMaterialsSize: 1024},
			MCPToolsSettings: mcp.ToolsSettings{
				WebSearchEnabled: enabled, WebFetchEnabled: enabled, ExtractKeyInfoEnabled: enabled,
			},
		}
		// This is the same builder used by NewResolver, without its unrelated
		// global General task-store setup. No generated schema is modified.
		mutation := (&Resolver{args: args}).buildMutationResolver()
		_, err := mutation.WebSearch(ctx, " \t ")
		require.ErrorContains(t, err, "query cannot be empty", "MCP enabled=%v", enabled)
		_, err = mutation.WebFetch(ctx, "http://127.0.0.1/private", nil)
		require.ErrorContains(t, err, "non-public", "MCP enabled=%v", enabled)
		_, err = mutation.ExtractKeyInfo(ctx, "", "materials", nil)
		require.ErrorContains(t, err, "query cannot be empty", "MCP enabled=%v", enabled)
	}
}
