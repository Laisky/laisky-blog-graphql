package web

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestRuntimeCatalogDoesNotUseMCPFlags tests the real dependency mapping without invoking providers.
func TestRuntimeCatalogDoesNotUseMCPFlags(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		resolver := &Resolver{args: ResolverArgs{
			Rdb: &rlibs.DB{}, RAGService: &rag.Service{}, FilesService: &files.Service{},
			MCPToolsSettings: mcp.ToolsSettings{WebFetchEnabled: enabled, ExtractKeyInfoEnabled: enabled, FileIOEnabled: enabled},
		}}
		catalog := buildInterfaceCatalog(interfaceSourcesFromResolver(resolver, nil))
		require.True(t, catalog.GraphQL["WebFetch"])
		require.True(t, catalog.GraphQL["ExtractKeyInfo"])
		require.True(t, catalog.HTTP["GET /tools/file_io/api/versions"])
		require.False(t, catalog.MCP["web_fetch"], "a flag alone does not register a tool")
		require.False(t, catalog.HTTP["PUT /tools/file_io/api/file"], "no selected writer is configured")
	}
}
