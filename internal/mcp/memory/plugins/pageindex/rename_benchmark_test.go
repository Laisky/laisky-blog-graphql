package pageindex

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// BenchmarkPageIndexSingleMoveLargeProjects measures a single-file move in a
// pair of projects with 100 existing 64 KiB trees each. This includes both
// project locks and real SQLite read/write and serialization costs.
func BenchmarkPageIndexSingleMoveLargeProjects(b *testing.B) {
	_, _, plugin := newCrossProjectPlugin(b)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "test-key", APIKeyHash: "benchmark-tenant", UserIdentity: "user:test"}
	large := strings.Repeat("a", 64<<10)
	for _, project := range []string{"alpha", "beta"} {
		storage := PageIndexStorageProject(auth, project)
		index := make(Index)
		for i := range 100 {
			name := fmt.Sprintf("/other-%03d.md", i)
			id := docIDFromAuth(auth, project, name)
			require.NoError(b, plugin.store.PutTree(ctx, storage, id, &Tree{DocID: id, Type: KindMarkdown, DocDescription: large}))
			index[name] = IndexEntry{DocID: id}
		}
		require.NoError(b, plugin.store.PutIndex(ctx, storage, index))
	}
	_, err := plugin.Write(ctx, auth, "alpha", "/move.md", "# Move\n\nA small document.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(b, err)
	source, dest := "alpha", "beta"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err = plugin.RenameAcrossProjects(ctx, auth, source, "/move.md", dest, "/move.md", false)
		require.NoError(b, err)
		source, dest = dest, source
	}
	b.StopTimer()
	for _, project := range []string{"alpha", "beta"} {
		storage := PageIndexStorageProject(auth, project)
		index, readErr := plugin.store.GetIndex(ctx, storage)
		require.NoError(b, readErr)
		for i := range 100 {
			id := index[fmt.Sprintf("/other-%03d.md", i)].DocID
			tree, treeErr := plugin.store.GetTree(ctx, storage, id)
			require.NoError(b, treeErr)
			require.Equal(b, len(large), len(tree.DocDescription))
		}
	}
}
