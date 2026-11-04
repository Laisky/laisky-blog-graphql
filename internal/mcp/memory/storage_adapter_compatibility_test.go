package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// compatibilityPlugin supplies controlled plugin responses at the production
// adapter boundary, including empty success and distinct storage failures.
type compatibilityPlugin struct {
	mcpplugin.Plugin
	search files.SearchResult
	list   files.ListResult
	err    error
}

// Search returns the controlled retrieval response.
func (p *compatibilityPlugin) Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error) {
	return p.search, p.err
}

// List returns the controlled listing response.
func (p *compatibilityPlugin) List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error) {
	return p.list, p.err
}

// TestStorageAdapterCompatibilityEmptyAndErrorContracts prevents successful
// empty retrievals from becoming failures, or backend failures from being hidden.
func TestStorageAdapterCompatibilityEmptyAndErrorContracts(t *testing.T) {
	ctx := context.Background()
	plugin := &compatibilityPlugin{}
	adapter, err := newStorageAdapter(plugin, files.AuthContext{APIKeyHash: "synthetic-hash"})
	require.NoError(t, err)
	chunks, err := adapter.Search(ctx, "project", "no-match", "", 10)
	require.NoError(t, err)
	require.NotNil(t, chunks)
	require.Empty(t, chunks)
	entries, more, err := adapter.List(ctx, "project", "/missing", 1, 10)
	require.NoError(t, err)
	require.NotNil(t, entries)
	require.Empty(t, entries)
	require.False(t, more)
	plugin.err = files.NewError(files.ErrCodeNotFound, "synthetic missing path", false)
	entries, more, err = adapter.List(ctx, "project", "/missing", 1, 10)
	require.NoError(t, err)
	require.NotNil(t, entries)
	require.Empty(t, entries)
	require.False(t, more)
	plugin.err = files.NewError(files.ErrCodePermissionDenied, "synthetic denied path", false)
	entries, more, err = adapter.List(ctx, "project", "/denied", 1, 10)
	require.Error(t, err)
	require.Nil(t, entries)
	require.False(t, more)
	chunks, err = adapter.Search(ctx, "project", "query", "", 10)
	require.ErrorIs(t, err, plugin.err)
	require.Nil(t, chunks)
	plugin.err = nil
	plugin.search.Chunks = []files.ChunkEntry{{FilePath: "/match.txt", FileSeekStartBytes: 3, FileSeekEndBytes: 8, ChunkContent: "match", Score: 0.7}}
	chunks, err = adapter.Search(ctx, "project", "match", "", 10)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, "/match.txt", chunks[0].FilePath)
	require.Equal(t, int64(3), chunks[0].StartBytes)
	require.Equal(t, int64(8), chunks[0].EndBytes)
	require.Equal(t, "match", chunks[0].Content)

	_, err = toFileWriteMode("unsupported")
	require.Error(t, err)
}

// TestMemorySummaryUpgradeBounds verifies the actual consumer delegates session
// summaries to the bounded SDK while preserving the documented empty-path flow.
func TestMemorySummaryUpgradeBounds(t *testing.T) {
	service, _ := newTestMemoryService(t)
	auth := files.AuthContext{APIKey: "synthetic-key", APIKeyHash: "synthetic-hash", UserIdentity: "synthetic-user"}
	req := ListDirWithAbstractRequest{Project: "demo", SessionID: "compat-session", Depth: 8, Limit: 200}
	out, err := service.ListDirWithAbstract(context.Background(), auth, req)
	require.NoError(t, err)
	require.NotEmpty(t, out.Summaries)
	require.Equal(t, "/memory/compat-session", out.Summaries[0].Path)
	for _, path := range []string{"/", "/memory/other-session", "/memory/compat-session/../other-session"} {
		t.Run(path, func(t *testing.T) {
			bad := req
			bad.Path = path
			result, listErr := service.ListDirWithAbstract(context.Background(), auth, bad)
			require.Error(t, listErr)
			require.Nil(t, result.Summaries)
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.ListDirWithAbstract(cancelled, auth, req)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result.Summaries)
}
