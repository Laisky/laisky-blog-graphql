package pageindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// renameTestAuth is the tenant used by the PageIndex rename tests.
var renameTestAuth = files.AuthContext{APIKey: "test-key", APIKeyHash: "tenant-a", UserIdentity: "user:test"}

// renameTargets runs a scenario as a same-project rename ("alpha") and as a
// cross-project move ("beta"); the source project is always "alpha".
var renameTargets = []string{"alpha", "beta"}

// renameCountingLLM counts model requests so tests can prove a rename sends none.
type renameCountingLLM struct {
	LLM
	calls atomic.Int64
}

// Respond counts the request and forwards it.
func (c *renameCountingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	c.calls.Add(1)
	return c.LLM.Respond(ctx, req)
}

// newCrossProjectPlugin starts a PageIndex plugin over a real SQLite service
// and the production transactional system handle. A supplied LLM also turns
// on node summaries, so every markdown index sends model requests.
func newCrossProjectPlugin(t testing.TB, llms ...LLM) (*sql.DB, *files.Service, *Plugin) {
	t.Helper()
	db, service := newPersistenceTestService(t)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	var llm LLM = NewStubLLM()
	if len(llms) != 0 {
		llm = llms[0]
		settings.Algo.GenerateNodeSummary = true
		settings.Indexer.TimeoutIndex = 10 * time.Second
	}
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: llm, Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, plugin.Stop(context.Background())) })
	return db, service, plugin
}

// newCountingRenamePlugin starts a plugin whose model requests are counted.
func newCountingRenamePlugin(t *testing.T) (*sql.DB, *files.Service, *Plugin, *renameCountingLLM) {
	t.Helper()
	stub := NewStubLLM()
	stub.SetDefault(&Response{
		Text:  `{"ranges":[{"start":1,"end":1000,"reason":"rename fixture"}]}`,
		Usage: Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	llm := &renameCountingLLM{LLM: stub}
	db, service, plugin := newCrossProjectPlugin(t, llm)
	return db, service, plugin, llm
}

// countSystemRows counts the active PageIndex system rows of one tenant namespace.
func countSystemRows(t testing.TB, db *sql.DB, storageProject string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE`,
		"system:"+SysOwner, storageProject, SysOwner,
	).Scan(&count))
	return count
}

// countTreeRows counts the active tree bodies of one tenant namespace.
func countTreeRows(t testing.TB, db *sql.DB, storageProject string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND system_owner = ? AND deleted = FALSE AND path NOT IN (?, ?)`,
		"system:"+SysOwner, storageProject, SysOwner, indexPath(), metaPath(),
	).Scan(&count))
	return count
}

// renameAndCheckFiles renames through the plugin without any model request and
// checks the authoritative result: the old path is gone and the new path holds
// the same bytes under the same incarnation with an advanced revision.
func renameAndCheckFiles(t *testing.T, plugin *Plugin, llm *renameCountingLLM, req files.RenameRequest) {
	t.Helper()
	ctx := context.Background()
	before, err := plugin.Stat(ctx, renameTestAuth, req.Project, req.FromPath)
	require.NoError(t, err)
	require.True(t, before.Exists)
	content, err := plugin.Read(ctx, renameTestAuth, req.Project, req.FromPath, 0, -1)
	require.NoError(t, err)
	calls := llm.calls.Load()

	result, err := plugin.Rename(ctx, renameTestAuth, req)
	require.NoError(t, err)
	require.Equal(t, 1, result.MovedCount)
	require.Equal(t, calls, llm.calls.Load(), "a rename sends no model request")

	_, err = plugin.Read(ctx, renameTestAuth, req.Project, req.FromPath, 0, -1)
	require.True(t, files.IsCode(err, files.ErrCodeNotFound), "%v", err)
	moved, err := plugin.Read(ctx, renameTestAuth, req.Destination(), req.ToPath, 0, -1)
	require.NoError(t, err)
	require.Equal(t, content.Content, moved.Content, "a rename never rewrites the bytes")
	after, err := plugin.Stat(ctx, renameTestAuth, req.Destination(), req.ToPath)
	require.NoError(t, err)
	beforeIdentity, beforeRevision, _ := strings.Cut(before.Version, ":")
	afterIdentity, afterRevision, _ := strings.Cut(after.Version, ":")
	require.Equal(t, beforeIdentity, afterIdentity, "the file keeps its incarnation")
	require.NotEqual(t, beforeRevision, afterRevision, "the revision advances so old tokens go stale")
}

// requireSearchHits asserts that a search of query under path returns hits
// for path only, each carrying the query text.
func requireSearchHits(t *testing.T, plugin *Plugin, project, query, path string) {
	t.Helper()
	result, err := plugin.Search(context.Background(), renameTestAuth, project, query, path, 10)
	require.NoError(t, err)
	require.NotEmpty(t, result.Chunks, "%s:%s", project, path)
	for _, chunk := range result.Chunks {
		require.Equal(t, path, chunk.FilePath)
		require.Contains(t, chunk.ChunkContent, query)
	}
}

// requireNoSearchHits asserts that project returns nothing under prefix.
func requireNoSearchHits(t *testing.T, plugin *Plugin, project, prefix string) {
	t.Helper()
	result, err := plugin.Search(context.Background(), renameTestAuth, project, "signal", prefix, 10)
	require.NoError(t, err)
	require.Empty(t, result.Chunks, "%s:%s", project, prefix)
}

// TestPageIndexRenameCarriesCurrentTree verifies that a rename keeps a current
// tree: it moves to the destination path's doc_id, the document stays
// searchable there without re-indexing, and nothing is left at the old path.
func TestPageIndexRenameCarriesCurrentTree(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			db, _, plugin, llm := newCountingRenamePlugin(t)
			ctx, auth := context.Background(), renameTestAuth
			_, err := plugin.Write(ctx, auth, "alpha", "/docs/manual.md", "# Manual\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			require.Positive(t, llm.calls.Load(), "indexing uses the model, so the rename check below is meaningful")

			renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: "/docs/manual.md", TargetProject: target, ToPath: "/archive/manual.md"})

			source, destination := PageIndexStorageProject(auth, "alpha"), PageIndexStorageProject(auth, target)
			index, err := plugin.store.GetIndex(ctx, destination)
			require.NoError(t, err)
			require.NotContains(t, index, "/docs/manual.md")
			entry := index["/archive/manual.md"]
			require.Equal(t, docIDFromAuth(auth, target, "/archive/manual.md"), entry.DocID, "the tree is re-keyed to the destination doc_id")
			tree, err := plugin.store.GetTree(ctx, destination, entry.DocID)
			require.NoError(t, err)
			require.Equal(t, entry.DocID, tree.DocID)
			require.Equal(t, files.HashFileContent([]byte("# Manual\n\nSignal quasar.")), tree.SourceContentHash)
			require.Equal(t, 1, countTreeRows(t, db, destination))
			if target != "alpha" {
				sourceIndex, err := plugin.store.GetIndex(ctx, source)
				require.NoError(t, err)
				require.Empty(t, sourceIndex)
				require.Zero(t, countTreeRows(t, db, source), "no tree stays behind")
			}
			requireSearchHits(t, plugin, target, "quasar", "/archive/manual.md")
			requireNoSearchHits(t, plugin, "alpha", "/docs")

			// The vacated path gets an independent identity.
			_, err = plugin.Write(ctx, auth, "alpha", "/docs/manual.md", "# Manual\n\nSignal pulsar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			requireSearchHits(t, plugin, target, "quasar", "/archive/manual.md")
			requireSearchHits(t, plugin, "alpha", "pulsar", "/docs/manual.md")
		})
	}
}

// TestPageIndexRenameDropsUnusableDerivedState verifies that a tree which is
// stale, missing or malformed, or a catalog entry that no longer matches the
// bytes, never makes a file unmovable. The file moves, its PageIndex state is
// dropped instead of carried, search returns nothing stale, and the next
// write indexes the file again.
func TestPageIndexRenameDropsUnusableDerivedState(t *testing.T) {
	faults := map[string]func(t *testing.T, plugin *Plugin, service *files.Service, docID string){
		"stale tree": func(t *testing.T, plugin *Plugin, _ *files.Service, docID string) {
			storage := PageIndexStorageProject(renameTestAuth, "alpha")
			tree, err := plugin.store.GetTree(context.Background(), storage, docID)
			require.NoError(t, err)
			tree.SourceContentHash = files.HashFileContent([]byte("older bytes"))
			require.NoError(t, plugin.store.PutTree(context.Background(), storage, docID, tree))
		},
		"missing tree": func(t *testing.T, plugin *Plugin, _ *files.Service, docID string) {
			require.NoError(t, plugin.store.DeleteTree(context.Background(), PageIndexStorageProject(renameTestAuth, "alpha"), docID))
		},
		"malformed tree": func(t *testing.T, plugin *Plugin, _ *files.Service, docID string) {
			require.NoError(t, plugin.sysFS.Write(context.Background(), PageIndexStorageProject(renameTestAuth, "alpha"), treePath(docID), []byte("{not json")))
		},
		"bytes changed outside PageIndex": func(t *testing.T, _ *Plugin, service *files.Service, _ string) {
			_, err := service.WriteWith(context.Background(), renameTestAuth, "alpha", "/doc.md", "# Doc\n\nRewritten signal pulsar.",
				"utf-8", 0, files.WriteModeTruncate, files.WriteOpts{SkipRAGIndex: true})
			require.NoError(t, err)
		},
	}
	for name, fault := range faults {
		for _, target := range renameTargets {
			t.Run(name+"/"+target, func(t *testing.T) {
				db, service, plugin, llm := newCountingRenamePlugin(t)
				ctx, auth := context.Background(), renameTestAuth
				_, err := plugin.Write(ctx, auth, "alpha", "/doc.md", "# Doc\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
				fault(t, plugin, service, docIDFromAuth(auth, "alpha", "/doc.md"))

				renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: "/doc.md", TargetProject: target, ToPath: "/moved.md"})

				for _, project := range []string{"alpha", target} {
					storage := PageIndexStorageProject(auth, project)
					index, err := plugin.store.GetIndex(ctx, storage)
					require.NoError(t, err)
					require.NotContains(t, index, "/doc.md")
					require.NotContains(t, index, "/moved.md")
					require.Zero(t, countTreeRows(t, db, storage), "the unusable tree is deleted, not orphaned")
				}
				requireNoSearchHits(t, plugin, "alpha", "")
				requireNoSearchHits(t, plugin, target, "")

				_, err = plugin.Write(ctx, auth, target, "/moved.md", "# Doc\n\nSignal nebula.", "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
				requireSearchHits(t, plugin, target, "nebula", "/moved.md")
			})
		}
	}
}

// TestPageIndexRenameLeavesMalformedCatalogUntouched verifies a catalog that
// cannot be decoded does not block the move and is not rewritten.
func TestPageIndexRenameLeavesMalformedCatalogUntouched(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			_, _, plugin, llm := newCountingRenamePlugin(t)
			ctx, auth := context.Background(), renameTestAuth
			_, err := plugin.Write(ctx, auth, "alpha", "/doc.md", "# Doc\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			source := PageIndexStorageProject(auth, "alpha")
			require.NoError(t, plugin.sysFS.Write(ctx, source, indexPath(), []byte("{not json")))

			renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: "/doc.md", TargetProject: target, ToPath: "/moved.md"})

			body, err := plugin.sysFS.Read(ctx, source, indexPath())
			require.NoError(t, err)
			require.Equal(t, "{not json", string(body))
			if target != "alpha" {
				index, err := plugin.store.GetIndex(ctx, PageIndexStorageProject(auth, target))
				require.NoError(t, err)
				require.Empty(t, index, "no entry is invented without a readable source")
				requireNoSearchHits(t, plugin, target, "")
			}
		})
	}
}

// TestPageIndexRenameAllowsExtensionChanges verifies ordinary extension
// changes, including from a document whose indexing failed permanently. The
// bytes are never converted; a destination of another kind holds no PageIndex
// state until it is written again.
func TestPageIndexRenameAllowsExtensionChanges(t *testing.T) {
	cases := []struct{ from, to, content string }{
		{from: "/notes.txt", to: "/notes.md", content: "Signal quasar in plain text."},
		{from: "/notes.md", to: "/notes.txt", content: "# Notes\n\nSignal quasar."},
		{from: "/notes.md", to: "/notes.pdf", content: "# Notes\n\nSignal quasar."},
		{from: "/scan.pdf", to: "/scan.md", content: "this is not a parsable pdf"},
	}
	for _, testCase := range cases {
		for _, target := range renameTargets {
			t.Run(testCase.from+"->"+testCase.to+"/"+target, func(t *testing.T) {
				db, _, plugin, llm := newCountingRenamePlugin(t)
				ctx, auth := context.Background(), renameTestAuth
				_, err := plugin.Write(ctx, auth, "alpha", testCase.from, testCase.content, "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err, "a document whose indexing fails is still stored")

				renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: testCase.from, TargetProject: target, ToPath: testCase.to})

				for _, project := range []string{"alpha", target} {
					storage := PageIndexStorageProject(auth, project)
					index, err := plugin.store.GetIndex(ctx, storage)
					require.NoError(t, err)
					require.Empty(t, index)
					require.Zero(t, countTreeRows(t, db, storage))
				}
				requireNoSearchHits(t, plugin, target, "")
			})
		}
	}
}

// waitingRenameLLM blocks the first model request until released.
type waitingRenameLLM struct {
	renameCountingLLM
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

// Respond blocks the first request, then forwards every request.
func (w *waitingRenameLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	w.once.Do(func() {
		close(w.started)
		select {
		case <-w.release:
		case <-ctx.Done():
		}
	})
	return w.renameCountingLLM.Respond(ctx, req)
}

// TestPageIndexRenameDuringPendingIndexing verifies a rename does not wait for
// a write whose tree is still being generated, holds no lock the model call
// needs, and that the late publication cannot resurrect the old path.
func TestPageIndexRenameDuringPendingIndexing(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			llm := &waitingRenameLLM{renameCountingLLM: renameCountingLLM{LLM: NewStubLLM()}, started: make(chan struct{}), release: make(chan struct{})}
			_, _, plugin := newCrossProjectPlugin(t, llm)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var release sync.Once
			defer release.Do(func() { close(llm.release) })
			auth := renameTestAuth
			writeDone := make(chan error, 1)
			go func() {
				_, err := plugin.Write(ctx, auth, "alpha", "/source.md", "# Source\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
				writeDone <- err
			}()
			select {
			case <-llm.started:
			case <-ctx.Done():
				t.Fatal("PageIndex did not begin tree generation")
			}

			renameAndCheckFiles(t, plugin, &llm.renameCountingLLM, files.RenameRequest{Project: "alpha", FromPath: "/source.md", TargetProject: target, ToPath: "/target.md"})

			release.Do(func() { close(llm.release) })
			select {
			case err := <-writeDone:
				require.NoError(t, err, "the late publication is skipped, not failed")
			case <-ctx.Done():
				t.Fatal("PageIndex did not complete tree generation")
			}
			for _, project := range []string{"alpha", target} {
				index, err := plugin.store.GetIndex(ctx, PageIndexStorageProject(auth, project))
				require.NoError(t, err)
				require.Empty(t, index, "a publication for the old path must not land")
			}
			requireNoSearchHits(t, plugin, "alpha", "")
			requireNoSearchHits(t, plugin, target, "")
		})
	}
}

// TestPageIndexRenameOverwriteDropsDisplacedTree verifies an overwritten
// destination loses its entry and tree, and the moved tree replaces it.
func TestPageIndexRenameOverwriteDropsDisplacedTree(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			db, _, plugin, llm := newCountingRenamePlugin(t)
			ctx, auth := context.Background(), renameTestAuth
			_, err := plugin.Write(ctx, auth, "alpha", "/source.md", "# Source\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			_, err = plugin.Write(ctx, auth, target, "/target.md", "# Target\n\nSignal nebula.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			_, err = plugin.Rename(ctx, auth, files.RenameRequest{Project: "alpha", FromPath: "/source.md", TargetProject: target, ToPath: "/target.md"})
			require.True(t, files.IsCode(err, files.ErrCodeAlreadyExists), "%v", err)

			renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: "/source.md", TargetProject: target, ToPath: "/target.md", Overwrite: true})

			storage := PageIndexStorageProject(auth, target)
			index, err := plugin.store.GetIndex(ctx, storage)
			require.NoError(t, err)
			require.Len(t, index, 1)
			require.Equal(t, 1, countTreeRows(t, db, storage), "the displaced tree is deleted")
			requireSearchHits(t, plugin, target, "quasar", "/target.md")
			result, err := plugin.Search(ctx, auth, target, "nebula", "", 10)
			require.NoError(t, err)
			for _, chunk := range result.Chunks {
				require.NotContains(t, chunk.ChunkContent, "nebula", "the displaced document is never returned")
			}
		})
	}
}

// TestPageIndexRenameDirectoryMixesCarriedAndDroppedState verifies one move of
// a directory carries the current tree, drops the stale one and moves files
// PageIndex does not index.
func TestPageIndexRenameDirectoryMixesCarriedAndDroppedState(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			db, service, plugin, llm := newCountingRenamePlugin(t)
			ctx, auth := context.Background(), renameTestAuth
			for path, content := range map[string]string{
				"/dir/current.md": "# Current\n\nSignal quasar.",
				"/dir/stale.md":   "# Stale\n\nSignal nebula.",
				"/dir/notes.txt":  "Signal notes.",
			} {
				_, err := plugin.Write(ctx, auth, "alpha", path, content, "utf-8", 0, files.WriteModeTruncate)
				require.NoError(t, err)
			}
			_, err := service.WriteWith(ctx, auth, "alpha", "/dir/stale.md", "# Stale\n\nSignal pulsar.", "utf-8", 0, files.WriteModeTruncate,
				files.WriteOpts{SkipRAGIndex: true})
			require.NoError(t, err)
			calls := llm.calls.Load()

			result, err := plugin.Rename(ctx, auth, files.RenameRequest{Project: "alpha", FromPath: "/dir", TargetProject: target, ToPath: "/moved"})
			require.NoError(t, err)
			require.Equal(t, 3, result.MovedCount)
			require.Equal(t, calls, llm.calls.Load())

			storage := PageIndexStorageProject(auth, target)
			index, err := plugin.store.GetIndex(ctx, storage)
			require.NoError(t, err)
			require.Equal(t, []string{"/moved/current.md"}, keysOf(index))
			require.Equal(t, 1, countTreeRows(t, db, storage))
			requireSearchHits(t, plugin, target, "quasar", "/moved/current.md")
			stale, err := plugin.Search(ctx, auth, target, "nebula", "", 10)
			require.NoError(t, err)
			for _, chunk := range stale.Chunks {
				require.NotContains(t, chunk.ChunkContent, "nebula")
			}
			requireNoSearchHits(t, plugin, "alpha", "/dir")
		})
	}
}

// keysOf returns the catalog paths.
func keysOf(index Index) []string {
	keys := make([]string, 0, len(index))
	for path := range index {
		keys = append(keys, path)
	}
	return keys
}

// plantLegacyEntry recreates the catalog shape of releases that kept the
// doc_id on rename: /legacy.md is indexed under the doc_id derived from /x.md.
func plantLegacyEntry(t *testing.T, plugin *Plugin, service *files.Service, project string) {
	t.Helper()
	ctx, auth := context.Background(), renameTestAuth
	_, err := plugin.Write(ctx, auth, project, "/x.md", "# Legacy\n\nSignal nebula.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = service.Rename(ctx, auth, files.RenameRequest{Project: project, FromPath: "/x.md", ToPath: "/legacy.md"})
	require.NoError(t, err)
	storage := PageIndexStorageProject(auth, project)
	index, err := plugin.store.GetIndex(ctx, storage)
	require.NoError(t, err)
	index["/legacy.md"] = index["/x.md"]
	delete(index, "/x.md")
	require.NoError(t, plugin.store.PutIndex(ctx, storage, index))
	requireSearchHits(t, plugin, project, "nebula", "/legacy.md")
}

// requireCatalogMatchesTrees asserts every entry uses the doc_id derived from
// its own path and that no tree is left without an entry.
func requireCatalogMatchesTrees(t *testing.T, db *sql.DB, plugin *Plugin, project string, paths ...string) {
	t.Helper()
	storage := PageIndexStorageProject(renameTestAuth, project)
	index, err := plugin.store.GetIndex(context.Background(), storage)
	require.NoError(t, err)
	require.ElementsMatch(t, paths, keysOf(index))
	for path, entry := range index {
		require.Equal(t, docIDFromAuth(renameTestAuth, project, path), entry.DocID, path)
	}
	require.Equal(t, len(paths), countTreeRows(t, db, storage), "no orphaned tree")
}

// TestPageIndexPathOwnsItsDerivedDocID verifies one identity rule for writes
// and moves: a path owns the doc_id derived from it. An older entry that still
// uses that doc_id is dropped when the path is written or receives a move, and
// rewriting such an entry deletes its old tree, so trees are never shared or
// orphaned and search never returns a tree under another document's path.
func TestPageIndexPathOwnsItsDerivedDocID(t *testing.T) {
	t.Run("write at the derived path", func(t *testing.T) {
		db, service, plugin, _ := newCountingRenamePlugin(t)
		plantLegacyEntry(t, plugin, service, "alpha")
		_, err := plugin.Write(context.Background(), renameTestAuth, "alpha", "/x.md", "# Fresh\n\nSignal pulsar.", "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
		requireSearchHits(t, plugin, "alpha", "pulsar", "/x.md")
		requireNoSearchHits(t, plugin, "alpha", "/legacy.md")
		requireCatalogMatchesTrees(t, db, plugin, "alpha", "/x.md")
	})
	t.Run("rewrite of the legacy entry", func(t *testing.T) {
		db, service, plugin, _ := newCountingRenamePlugin(t)
		plantLegacyEntry(t, plugin, service, "alpha")
		_, err := plugin.Write(context.Background(), renameTestAuth, "alpha", "/legacy.md", "# Legacy\n\nSignal comet.", "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
		requireSearchHits(t, plugin, "alpha", "comet", "/legacy.md")
		requireCatalogMatchesTrees(t, db, plugin, "alpha", "/legacy.md")
	})
	for _, target := range renameTargets {
		t.Run("move onto the derived path/"+target, func(t *testing.T) {
			db, service, plugin, llm := newCountingRenamePlugin(t)
			plantLegacyEntry(t, plugin, service, target)
			_, err := plugin.Write(context.Background(), renameTestAuth, "alpha", "/a.md", "# Incoming\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)

			renameAndCheckFiles(t, plugin, llm, files.RenameRequest{Project: "alpha", FromPath: "/a.md", TargetProject: target, ToPath: "/x.md"})

			requireSearchHits(t, plugin, target, "quasar", "/x.md")
			requireNoSearchHits(t, plugin, target, "/legacy.md")
			requireCatalogMatchesTrees(t, db, plugin, target, "/x.md")
		})
	}
}

// TestPageIndexRenameLeavesAFullCatalogUntouched verifies the file size limit
// of the catalog never fails a move: the destination catalog that cannot take
// the entry is left as it is, and the moved file is simply not indexed.
func TestPageIndexRenameLeavesAFullCatalogUntouched(t *testing.T) {
	db, service := newPersistenceTestServiceWithLimit(t, 3000)
	settings := persistenceTestSettings()
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: NewStubLLM(), Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, plugin.Stop(context.Background())) })
	ctx, auth := context.Background(), renameTestAuth

	beta := PageIndexStorageProject(auth, "beta")
	var full []byte
	for i := 0; ; i++ {
		if _, err := plugin.Write(ctx, auth, "beta", fmt.Sprintf("/f%02d.md", i), "# D\n\nx", "utf-8", 0, files.WriteModeTruncate); err != nil {
			_, err = service.Delete(ctx, auth, "beta", fmt.Sprintf("/f%02d.md", i), false)
			require.NoError(t, err)
			break
		}
		full, err = plugin.sysFS.Read(ctx, beta, indexPath())
		require.NoError(t, err)
	}
	_, err = plugin.Write(ctx, auth, "alpha", "/a.md", "# D\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)

	_, err = plugin.Rename(ctx, auth, files.RenameRequest{Project: "alpha", FromPath: "/a.md", TargetProject: "beta", ToPath: "/moved.md"})
	require.NoError(t, err, "a full catalog never fails a valid move")
	read, err := service.Read(ctx, auth, "beta", "/moved.md", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "# D\n\nSignal quasar.", read.Content)
	after, err := plugin.sysFS.Read(ctx, beta, indexPath())
	require.NoError(t, err)
	require.Equal(t, string(full), string(after))
	require.Zero(t, countTreeRows(t, db, PageIndexStorageProject(auth, "alpha")), "the source state is still removed")
	requireNoSearchHits(t, plugin, "beta", "/moved.md")
}

// TestPageIndexRenameWithoutIndexer verifies a rename needs no running
// indexer or model: a plugin built without an LLM still moves files and
// carries their current trees.
func TestPageIndexRenameWithoutIndexer(t *testing.T) {
	for _, target := range renameTargets {
		t.Run(target, func(t *testing.T) {
			_, service, indexing := newCrossProjectPlugin(t)
			ctx, auth := context.Background(), renameTestAuth
			_, err := indexing.Write(ctx, auth, "alpha", "/doc.md", "# Doc\n\nSignal quasar.", "utf-8", 0, files.WriteModeTruncate)
			require.NoError(t, err)
			systemFS, err := service.SystemNamespace(SysOwner)
			require.NoError(t, err)
			idle, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: persistenceTestSettings()})
			require.NoError(t, err)
			require.Error(t, idle.Start(ctx), "without an LLM the plugin cannot start")

			_, err = idle.Rename(ctx, auth, files.RenameRequest{Project: "alpha", FromPath: "/doc.md", TargetProject: target, ToPath: "/moved.md"})
			require.NoError(t, err)
			read, err := service.Read(ctx, auth, target, "/moved.md", 0, -1)
			require.NoError(t, err)
			require.Equal(t, "# Doc\n\nSignal quasar.", read.Content)
			requireSearchHits(t, indexing, target, "quasar", "/moved.md")
		})
	}
}

// TestPageIndexRenameIdentityFollowsCatalog verifies rewrites and reuse of an
// old path do not invalidate a different live document.
func TestPageIndexRenameIdentityFollowsCatalog(t *testing.T) {
	db, _, plugin := newCrossProjectPlugin(t)
	ctx, auth := context.Background(), renameTestAuth
	write := func(project, path, body string) {
		t.Helper()
		_, err := plugin.Write(ctx, auth, project, path, "# Document\n\n"+body, "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err)
	}
	rename := func(req files.RenameRequest) {
		t.Helper()
		_, err := plugin.Rename(ctx, auth, req)
		require.NoError(t, err)
	}
	write("beta", "/x.md", "nebula")
	rename(files.RenameRequest{Project: "beta", FromPath: "/x.md", ToPath: "/other.md"})
	write("alpha", "/incoming.md", "quasar")
	rename(files.RenameRequest{Project: "alpha", FromPath: "/incoming.md", TargetProject: "beta", ToPath: "/x.md"})
	write("beta", "/x.md", "pulsar")
	requireSearchHits(t, plugin, "beta", "nebula", "/other.md")
	requireSearchHits(t, plugin, "beta", "pulsar", "/x.md")

	rename(files.RenameRequest{Project: "beta", FromPath: "/other.md", ToPath: "/third.md"})
	write("beta", "/other.md", "andromeda")
	requireSearchHits(t, plugin, "beta", "nebula", "/third.md")
	requireSearchHits(t, plugin, "beta", "andromeda", "/other.md")
	_, err := plugin.Delete(ctx, auth, "beta", "/x.md", false)
	require.NoError(t, err)
	requireSearchHits(t, plugin, "beta", "nebula", "/third.md")
	require.Equal(t, 4, countSystemRows(t, db, PageIndexStorageProject(auth, "beta")), "two trees, catalog and metadata")
}
