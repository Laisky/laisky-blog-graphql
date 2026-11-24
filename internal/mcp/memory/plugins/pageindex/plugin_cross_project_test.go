package pageindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// newCrossProjectPlugin starts a PageIndex plugin over a real SQLite service
// and the production transactional system handle.
func newCrossProjectPlugin(t *testing.T) (*sql.DB, *files.Service, *Plugin) {
	t.Helper()
	db, service := newPersistenceTestService(t)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: NewStubLLM(), Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, plugin.Stop(context.Background())) })
	return db, service, plugin
}

// countSystemRows counts the active PageIndex system rows of one tenant namespace.
func countSystemRows(t *testing.T, db *sql.DB, storageProject string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`,
		"system:"+SysOwner, storageProject, SysOwner,
	).Scan(&count))
	return count
}

// TestPageIndexRenameAcrossProjectsMovesTreeAndCatalog verifies that a
// cross-project move carries the tree and catalog entry into the target tenant
// namespace atomically, keeps the document searchable there without
// re-indexing, and leaves no tree behind in the source namespace.
func TestPageIndexRenameAcrossProjectsMovesTreeAndCatalog(t *testing.T) {
	db, _, plugin := newCrossProjectPlugin(t)
	auth := files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}
	ctx := context.Background()
	content := "# Manual\n\nThe cross project document token is ORBIT-17."
	_, err := plugin.Write(ctx, auth, "alpha", "/manual.md", content, "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	alphaStorage, betaStorage := PageIndexStorageProject(auth, "alpha"), PageIndexStorageProject(auth, "beta")
	require.Equal(t, 3, countSystemRows(t, db, alphaStorage))

	result, err := plugin.RenameAcrossProjects(ctx, auth, "alpha", "/manual.md", "beta", "/docs/manual.md", false)
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)

	read, err := plugin.Read(ctx, auth, "beta", "/docs/manual.md", 0, -1)
	require.NoError(t, err)
	require.Equal(t, content, read.Content)
	_, err = plugin.Read(ctx, auth, "alpha", "/manual.md", 0, -1)
	require.True(t, files.IsCode(err, files.ErrCodeNotFound), "%v", err)

	alphaIndex, err := plugin.store.GetIndex(ctx, alphaStorage)
	require.NoError(t, err)
	require.Empty(t, alphaIndex)
	require.Equal(t, 2, countSystemRows(t, db, alphaStorage), "the source keeps only its empty catalog and metadata")

	betaIndex, err := plugin.store.GetIndex(ctx, betaStorage)
	require.NoError(t, err)
	entry, ok := betaIndex["/docs/manual.md"]
	require.True(t, ok)
	require.Equal(t, docIDFromAuth(auth, "beta", "/docs/manual.md"), entry.DocID, "the tree is re-keyed to the target path's doc_id")
	tree, err := plugin.store.GetTree(ctx, betaStorage, entry.DocID)
	require.NoError(t, err)
	require.Equal(t, entry.DocID, tree.DocID)
	require.Equal(t, files.HashFileContent([]byte(content)), tree.SourceContentHash)
	require.Equal(t, 3, countSystemRows(t, db, betaStorage))

	moved, err := plugin.Search(ctx, auth, "beta", "document", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, moved.Chunks, "the moved document is searchable in the target project without re-indexing")
	stale, err := plugin.Search(ctx, auth, "alpha", "document", "", 10)
	require.NoError(t, err)
	require.Empty(t, stale.Chunks)

	// A later write at the moved path reuses the same doc_id, so no tree is orphaned.
	_, err = plugin.Write(ctx, auth, "beta", "/docs/manual.md", "# Manual\n\nRewritten after the move.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, 3, countSystemRows(t, db, betaStorage))
}

// TestPageIndexRenameAcrossProjectsOverwrite verifies that an overwritten
// target document loses its catalog entry and tree in the same transaction.
func TestPageIndexRenameAcrossProjectsOverwrite(t *testing.T) {
	db, _, plugin := newCrossProjectPlugin(t)
	auth := files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}
	ctx := context.Background()
	source := "# Source\n\nThe incoming document."
	_, err := plugin.Write(ctx, auth, "alpha", "/manual.md", source, "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	// The target entry keeps the doc_id of its original path, so the replaced
	// tree is distinct from the one the move stores.
	_, err = plugin.Write(ctx, auth, "beta", "/original.md", "# Target\n\nThe replaced document.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = plugin.Rename(ctx, auth, "beta", "/original.md", "/manual.md", false)
	require.NoError(t, err)
	betaStorage := PageIndexStorageProject(auth, "beta")
	require.Equal(t, 3, countSystemRows(t, db, betaStorage))

	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/manual.md", "beta", "/manual.md", false)
	require.True(t, files.IsCode(err, files.ErrCodeAlreadyExists), "%v", err)

	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/manual.md", "beta", "/manual.md", true)
	require.NoError(t, err)
	betaIndex, err := plugin.store.GetIndex(ctx, betaStorage)
	require.NoError(t, err)
	require.Len(t, betaIndex, 1)
	tree, err := plugin.store.GetTree(ctx, betaStorage, betaIndex["/manual.md"].DocID)
	require.NoError(t, err)
	require.Equal(t, files.HashFileContent([]byte(source)), tree.SourceContentHash, "the target tree describes the moved document")
	require.Equal(t, 3, countSystemRows(t, db, betaStorage), "the replaced tree must not be orphaned")
}

// TestPageIndexRenameAcrossProjectsRequiresAtomicCatalog verifies that a
// non-transactional catalog refuses the move before any user file changes.
func TestPageIndexRenameAcrossProjectsRequiresAtomicCatalog(t *testing.T) {
	_, service := newPersistenceTestService(t)
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: newMemoryFS(), Settings: settings, LLM: NewStubLLM(), Tokenizer: tokenizer})
	require.NoError(t, err)
	auth := files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}
	ctx := context.Background()
	_, err = service.Write(ctx, auth, "alpha", "/note.txt", "note", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)

	_, err = plugin.RenameAcrossProjects(ctx, auth, "alpha", "/note.txt", "beta", "/note.txt", false)
	require.True(t, files.IsCode(err, files.ErrCodeInvalidArgument), "%v", err)
	read, err := service.Read(ctx, auth, "alpha", "/note.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "note", read.Content)
}

// TestPageIndexMoveStateDocIDAllocation verifies that a moved tree never takes
// over a doc_id that a remaining target entry references, and that a tree still
// referenced in the source namespace is kept there.
func TestPageIndexMoveStateDocIDAllocation(t *testing.T) {
	auth := files.AuthContext{APIKeyHash: "tenant-a"}
	targetDocID := func(userPath string) string { return docIDFromAuth(auth, "beta", userPath) }
	preferred := targetDocID("/x.md")
	tree := func(docID string) []byte {
		body, err := json.Marshal(&Tree{DocID: docID, Type: KindMarkdown})
		require.NoError(t, err)
		return body
	}
	catalog := func(index Index) []byte {
		body, err := json.Marshal(index)
		require.NoError(t, err)
		return body
	}
	decode := func(state map[string][]byte) Index {
		index, err := pageIndexDecodeState(state)
		require.NoError(t, err)
		return index
	}

	t.Run("preferred doc_id taken falls back to the original", func(t *testing.T) {
		source := map[string][]byte{
			indexPath():      catalog(Index{"/a.md": {DocID: "orig"}, "/shared.md": {DocID: "orig"}}),
			treePath("orig"): tree("orig"),
		}
		target := map[string][]byte{
			indexPath():         catalog(Index{"/other.md": {DocID: preferred}}),
			treePath(preferred): tree(preferred),
		}
		require.NoError(t, pageIndexMoveState("/a.md", "/x.md", false, targetDocID)(source, target))
		targetIndex := decode(target)
		require.Equal(t, "orig", targetIndex["/x.md"].DocID)
		require.Equal(t, preferred, targetIndex["/other.md"].DocID)
		require.Contains(t, target, treePath(preferred), "the other entry's tree is untouched")
		require.Contains(t, target, treePath("orig"))
		require.Contains(t, source, treePath("orig"), "a tree another source entry still references stays")
		require.NotContains(t, decode(source), "/a.md")
	})

	t.Run("both candidates taken uses a salted doc_id", func(t *testing.T) {
		source := map[string][]byte{indexPath(): catalog(Index{"/a.md": {DocID: "orig"}}), treePath("orig"): tree("orig")}
		target := map[string][]byte{indexPath(): catalog(Index{"/one.md": {DocID: preferred}, "/two.md": {DocID: "orig"}})}
		require.NoError(t, pageIndexMoveState("/a.md", "/x.md", false, targetDocID)(source, target))
		docID := decode(target)["/x.md"].DocID
		require.NotContains(t, []string{preferred, "orig"}, docID)
		var moved Tree
		require.NoError(t, json.Unmarshal(target[treePath(docID)], &moved))
		require.Equal(t, docID, moved.DocID, "the stored tree names its new doc_id")
		require.NotContains(t, source, treePath("orig"))
	})

	t.Run("nothing indexed leaves both namespaces untouched", func(t *testing.T) {
		source := map[string][]byte{}
		target := map[string][]byte{}
		require.NoError(t, pageIndexMoveState("/a.txt", "/a.txt", true, targetDocID)(source, target))
		require.Empty(t, source)
		require.Empty(t, target)
	})
}
