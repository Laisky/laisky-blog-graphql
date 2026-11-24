package pageindex

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestPageIndexFollowsDefaultProjectLayer verifies that PageIndex trees and
// catalogs live with the physical project that stores a file: a write-through
// to a default re-indexes the default, an override is indexed by its project,
// and search reads the same merged view as file_read.
func TestPageIndexFollowsDefaultProjectLayer(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: NewStubLLM(), Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	defaultCatalog := PageIndexStorageProject(auth, files.DefaultProject)
	projectCatalog := PageIndexStorageProject(auth, "project")
	catalog := func(storageProject string) Index {
		t.Helper()
		index, err := plugin.store.GetIndex(ctx, storageProject)
		require.NoError(t, err)
		return index
	}
	searchText := func(project string) string {
		t.Helper()
		result, err := plugin.Search(ctx, auth, project, "token", "", 10)
		require.NoError(t, err)
		var texts []string
		for _, chunk := range result.Chunks {
			require.Empty(t, chunk.Project, "single-project hits never name the layer")
			texts = append(texts, chunk.FilePath+"="+chunk.ChunkContent)
		}
		return strings.Join(texts, "\n")
	}

	_, err = plugin.Write(ctx, auth, files.DefaultProject, "/guide.md", "# Guide\n\nThe default token is NEBULA-9.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Contains(t, catalog(defaultCatalog), "/guide.md")
	require.Contains(t, searchText("project"), "NEBULA-9", "a project searches the defaults it inherits")

	// Write-through re-indexes the default; the project's catalog stays empty.
	res, err := plugin.Write(ctx, auth, "project", "/guide.md", "# Guide\n\nThe shared token is COMET-4.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, files.DefaultProject, res.ResolvedProject)
	require.NotContains(t, catalog(projectCatalog), "/guide.md")
	other := searchText("other")
	require.Contains(t, other, "COMET-4")
	require.NotContains(t, other, "NEBULA-9")

	// An override renamed over the default is indexed by its own project and
	// hides the default only there.
	_, err = plugin.Write(ctx, auth, "project", "/local.md", "# Guide\n\nThe project token is PULSAR-2.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = plugin.Rename(ctx, auth, "project", "/local.md", "/guide.md", true)
	require.NoError(t, err)
	require.Contains(t, catalog(projectCatalog), "/guide.md")
	require.NotContains(t, catalog(projectCatalog), "/local.md")
	require.Contains(t, catalog(defaultCatalog), "/guide.md", "the shadowed default keeps its tree")
	own := searchText("project")
	require.Contains(t, own, "PULSAR-2")
	require.NotContains(t, own, "COMET-4", "a shadowed default never answers for the project")
	require.Contains(t, searchText("other"), "COMET-4")

	// Deleting the override drops only the project's tree; deleting again removes
	// the default and its tree for every project.
	_, err = plugin.Delete(ctx, auth, "project", "/guide.md", false)
	require.NoError(t, err)
	require.NotContains(t, catalog(projectCatalog), "/guide.md")
	require.Contains(t, searchText("project"), "COMET-4")
	_, err = plugin.Delete(ctx, auth, "project", "/guide.md", false)
	require.NoError(t, err)
	require.NotContains(t, catalog(defaultCatalog), "/guide.md")
	require.Empty(t, searchText("other"))
}

// TestPageIndexStaleProjectEntryDoesNotHideDefault covers a project catalog
// entry left behind when its file was deleted outside PageIndex (for example by
// the RAG plugin). Search must fall back to the visible default, not go blind.
func TestPageIndexStaleProjectEntryDoesNotHideDefault(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: NewStubLLM(), Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}

	_, err = plugin.Write(ctx, auth, "project", "/doc.md", "# Doc\n\nThe project token is QUASAR-1.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = service.Delete(ctx, auth, "project", "/doc.md", false) // bypasses the PageIndex catalog
	require.NoError(t, err)
	_, err = plugin.Write(ctx, auth, files.DefaultProject, "/doc.md", "# Doc\n\nThe default token is AURORA-5.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Contains(t, catalogPaths(t, plugin, PageIndexStorageProject(auth, "project")), "/doc.md", "precondition: a stale project entry")

	result, err := plugin.Search(ctx, auth, "project", "token", "", 10)
	require.NoError(t, err)
	var texts []string
	for _, chunk := range result.Chunks {
		texts = append(texts, chunk.ChunkContent)
	}
	joined := strings.Join(texts, "\n")
	require.Contains(t, joined, "AURORA-5")
	require.NotContains(t, joined, "QUASAR-1")
}

// catalogPaths lists the user paths of one storage project's catalog.
func catalogPaths(t *testing.T, plugin *Plugin, storageProject string) []string {
	t.Helper()
	index, err := plugin.store.GetIndex(context.Background(), storageProject)
	require.NoError(t, err)
	paths := make([]string, 0, len(index))
	for userPath := range index {
		paths = append(paths, userPath)
	}
	return paths
}
