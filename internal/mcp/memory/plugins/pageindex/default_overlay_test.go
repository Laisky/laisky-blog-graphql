package pageindex

import (
	"context"
	"database/sql"
	"regexp"
	"sort"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// The tests below pin the physical retrieval contract of the DefaultProject
// layer (docs/proposals/fileio_default_project.md): FileIO reads a merged view,
// but PageIndex indexes and searches physical projects. A search of an ordinary
// project reads its own catalog and the DefaultProject catalog, every catalog
// entry is an independent candidate validated against its own physical row, and
// every hit names the physical project that stores it.

// overlayHit is one search hit reduced to its physical project, path and the
// unique token of the document it came from.
type overlayHit struct {
	Project string
	Path    string
	Token   string
}

// overlayToken matches the unique token every test document carries.
var overlayToken = regexp.MustCompile(`[A-Z]+-[0-9]+`)

// overlayHarness drives a PageIndex plugin over a real SQLite FileIO service and
// the production transactional system handle, as tenant "tenant-a".
type overlayHarness struct {
	t       *testing.T
	ctx     context.Context
	auth    files.AuthContext
	service *files.Service
	plugin  *Plugin
}

func newDefaultOverlayHarness(t *testing.T) *overlayHarness {
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
	return &overlayHarness{
		t: t, ctx: context.Background(), service: service, plugin: plugin,
		auth: files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"},
	}
}

// overlayDoc is a markdown document carrying token.
func overlayDoc(token string) string {
	return "# Guide\n\nThe token is " + token + "."
}

// write stores a document through PageIndex and returns the FileIO result.
func (h *overlayHarness) write(project, path, token string) files.WriteResult {
	h.t.Helper()
	res, err := h.plugin.Write(h.ctx, h.auth, project, path, overlayDoc(token), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(h.t, err)
	return res
}

// override creates project's own file at path over an inherited default the
// supported way: a new name in project renamed onto the default's path.
func (h *overlayHarness) override(project, path, token string) {
	h.t.Helper()
	res := h.write(project, "/override-staging.md", token)
	require.Equal(h.t, project, res.ResolvedProject, "a new name is created in the project")
	_, err := h.plugin.Rename(h.ctx, h.auth, project, "/override-staging.md", path, true)
	require.NoError(h.t, err)
}

// catalog returns the PageIndex catalog of one physical project.
func (h *overlayHarness) catalog(project string) Index {
	h.t.Helper()
	index, err := h.plugin.store.GetIndex(h.ctx, PageIndexStorageProject(h.auth, project))
	require.NoError(h.t, err)
	return index
}

// tree loads the tree a catalog entry of one physical project points at.
func (h *overlayHarness) tree(project, path string) *Tree {
	h.t.Helper()
	entry, ok := h.catalog(project)[path]
	require.True(h.t, ok, "%s has no catalog entry for %s", project, path)
	tree, err := h.plugin.store.GetTree(h.ctx, PageIndexStorageProject(h.auth, project), entry.DocID)
	require.NoError(h.t, err)
	require.Equal(h.t, entry.SourceContentHash, tree.SourceContentHash)
	return tree
}

// treeGone asserts that a tree no longer exists in a physical project's namespace.
func (h *overlayHarness) treeGone(project, docID string) {
	h.t.Helper()
	_, err := h.plugin.store.GetTree(h.ctx, PageIndexStorageProject(h.auth, project), docID)
	require.Error(h.t, err, "tree %s of %s must be deleted", docID, project)
}

// search runs a PageIndex search of project and returns every hit, sorted.
// Every chunk must name a document token, so a hit is never anonymous.
func (h *overlayHarness) search(project string) []overlayHit {
	h.t.Helper()
	result, err := h.plugin.Search(h.ctx, h.auth, project, "token", "", 20)
	require.NoError(h.t, err)
	hits := make([]overlayHit, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		token := overlayToken.FindString(chunk.ChunkContent)
		require.NotEmpty(h.t, token, "hit %s:%s carries no document token: %q", chunk.Project, chunk.FilePath, chunk.ChunkContent)
		hits = append(hits, overlayHit{Project: chunk.Project, Path: chunk.FilePath, Token: token})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Project != hits[j].Project {
			return hits[i].Project < hits[j].Project
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Token < hits[j].Token
	})
	return hits
}

// read returns the token of the file project shows at path.
func (h *overlayHarness) read(project, path string) string {
	h.t.Helper()
	res, err := h.service.Read(h.ctx, h.auth, project, path, 0, -1)
	require.NoError(h.t, err)
	return overlayToken.FindString(res.Content)
}

// readMissing asserts that project shows no file at path.
func (h *overlayHarness) readMissing(project, path string) {
	h.t.Helper()
	_, err := h.service.Read(h.ctx, h.auth, project, path, 0, -1)
	require.True(h.t, files.IsCode(err, files.ErrCodeNotFound), "%s:%s must be absent, got %v", project, path, err)
}

// requireHitsReadable asserts that every hit of a search of searched reads back
// its own document when read from the project it names.
func (h *overlayHarness) requireHitsReadable(searched string, hits []overlayHit) {
	h.t.Helper()
	for _, hit := range hits {
		source := hit.Project
		if source == "" {
			source = searched
		}
		require.Equal(h.t, hit.Token, h.read(source, hit.Path), "hit %+v of %s must read back from %s", hit, searched, source)
	}
}

// TestPageIndexDefaultOverlaySamePathDocumentsAreBothHits verifies that a
// project override and the default it hides at the same path are two hits of
// the project's search, each labeled with and readable from its physical
// project, while other projects and DefaultProject itself see only the default.
func TestPageIndexDefaultOverlaySamePathDocumentsAreBothHits(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write(files.DefaultProject, "/guide.md", "NEBULA-9")
	h.override("work", "/guide.md", "PULSAR-2")
	require.Equal(t, "PULSAR-2", h.read("work", "/guide.md"), "the project's view shows its override")
	require.Contains(t, h.catalog("work"), "/guide.md")
	require.NotContains(t, h.catalog("work"), "/override-staging.md")
	require.Contains(t, h.catalog(files.DefaultProject), "/guide.md", "the shadowed default keeps its own tree")

	work := h.search("work")
	require.Equal(t, []overlayHit{
		{Project: files.DefaultProject, Path: "/guide.md", Token: "NEBULA-9"},
		{Project: "work", Path: "/guide.md", Token: "PULSAR-2"},
	}, work, "a shadowed default is still a hit of the project, labeled with its physical project")
	h.requireHitsReadable("work", work)

	other := h.search("other")
	require.Equal(t, []overlayHit{{Project: files.DefaultProject, Path: "/guide.md", Token: "NEBULA-9"}}, other)
	h.requireHitsReadable("other", other)

	base := h.search(files.DefaultProject)
	require.Equal(t, []overlayHit{{Project: "", Path: "/guide.md", Token: "NEBULA-9"}}, base, "a single-catalog search never labels its hits")
	h.requireHitsReadable(files.DefaultProject, base)
}

// TestPageIndexDefaultOverlayIdenticalContentStaysTwoHits verifies that
// identical bytes at the same path in the project and DefaultProject are not
// deduplicated: each catalog keeps its own tree and yields its own hit.
func TestPageIndexDefaultOverlayIdenticalContentStaysTwoHits(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write(files.DefaultProject, "/same.md", "TWIN-1")
	h.override("work", "/same.md", "TWIN-1")

	workEntry, defaultEntry := h.catalog("work")["/same.md"], h.catalog(files.DefaultProject)["/same.md"]
	require.NotEqual(t, workEntry.DocID, defaultEntry.DocID, "each physical project owns a distinct tree")
	require.Equal(t, workEntry.SourceContentHash, defaultEntry.SourceContentHash)
	require.Equal(t, files.HashFileContent([]byte(overlayDoc("TWIN-1"))), h.tree("work", "/same.md").SourceContentHash)
	require.Equal(t, files.HashFileContent([]byte(overlayDoc("TWIN-1"))), h.tree(files.DefaultProject, "/same.md").SourceContentHash)

	require.Equal(t, []overlayHit{
		{Project: files.DefaultProject, Path: "/same.md", Token: "TWIN-1"},
		{Project: "work", Path: "/same.md", Token: "TWIN-1"},
	}, h.search("work"))
}

// TestPageIndexDefaultOverlayAncestorFileDoesNotHideDefaultHit verifies that a
// default the project's view hides behind a project file at an ancestor path
// is still a search hit, labeled and readable from DefaultProject.
func TestPageIndexDefaultOverlayAncestorFileDoesNotHideDefaultHit(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	_, err := h.service.Write(h.ctx, h.auth, "work", "/sub", "a plain project file", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	h.write(files.DefaultProject, "/sub/deep.md", "VEGA-3")
	h.readMissing("work", "/sub/deep.md") // precondition: the view hides the default below the project's file

	hits := h.search("work")
	require.Equal(t, []overlayHit{{Project: files.DefaultProject, Path: "/sub/deep.md", Token: "VEGA-3"}}, hits)
	h.requireHitsReadable("work", hits)
}

// TestPageIndexDefaultOverlayStaleProjectEntryIsSkipped covers a project
// catalog entry left behind when its file was deleted outside PageIndex. The
// stale entry is validated against the project's own row and skipped, also when
// the default at the same path has the same bytes; the default entry is still a
// hit of its own, labeled DefaultProject.
func TestPageIndexDefaultOverlayStaleProjectEntryIsSkipped(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write("work", "/doc.md", "QUASAR-1")
	h.write("work", "/twin.md", "ECHO-7")
	for _, path := range []string{"/doc.md", "/twin.md"} {
		_, err := h.service.Delete(h.ctx, h.auth, "work", path, false) // bypasses the PageIndex catalog
		require.NoError(t, err)
	}
	h.write(files.DefaultProject, "/doc.md", "AURORA-5")
	h.write(files.DefaultProject, "/twin.md", "ECHO-7") // the stale entry's exact bytes
	require.Contains(t, h.catalog("work"), "/doc.md", "precondition: stale project entries")
	require.Contains(t, h.catalog("work"), "/twin.md", "precondition: stale project entries")
	require.Equal(t, h.catalog("work")["/twin.md"].SourceContentHash, h.catalog(files.DefaultProject)["/twin.md"].SourceContentHash)

	// The project's view shows the defaults, but the project stores no row.
	for path, token := range map[string]string{"/doc.md": "AURORA-5", "/twin.md": "ECHO-7"} {
		require.Equal(t, token, h.read("work", path))
		_, err := h.service.ActiveContentHash(h.ctx, h.auth, "work", path)
		require.True(t, errors.Is(err, sql.ErrNoRows), "%s: %v", path, err)
	}

	hits := h.search("work")
	require.Equal(t, []overlayHit{
		{Project: files.DefaultProject, Path: "/doc.md", Token: "AURORA-5"},
		{Project: files.DefaultProject, Path: "/twin.md", Token: "ECHO-7"},
	}, hits, "a stale project entry never answers, not even through the default's bytes")
	h.requireHitsReadable("work", hits)
}

// TestPageIndexDefaultOverlayCandidateBudgetSpansBothCatalogs verifies that
// tree_query.candidate_docs bounds the combined candidates of both catalogs,
// ordered by path and then the project's catalog before DefaultProject's.
func TestPageIndexDefaultOverlayCandidateBudgetSpansBothCatalogs(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	require.Equal(t, 5, h.plugin.cfg.TreeQuery.CandidateDocs, "precondition: the harness budget")
	for path, token := range map[string]string{"/a.md": "ALPHA-1", "/c.md": "GAMMA-3", "/e.md": "EPSILON-5"} {
		h.write("work", path, token) // the project's own files, written before any default
	}
	for path, token := range map[string]string{"/b.md": "BETA-2", "/d.md": "DELTA-4", "/e.md": "ZETA-6"} {
		h.write(files.DefaultProject, path, token)
	}

	// Each catalog alone fits the budget; combined, the sixth candidate
	// (DefaultProject's /e.md, tied by path with the project's) is cut.
	require.Equal(t, []overlayHit{
		{Project: files.DefaultProject, Path: "/b.md", Token: "BETA-2"},
		{Project: files.DefaultProject, Path: "/d.md", Token: "DELTA-4"},
		{Project: "work", Path: "/a.md", Token: "ALPHA-1"},
		{Project: "work", Path: "/c.md", Token: "GAMMA-3"},
		{Project: "work", Path: "/e.md", Token: "EPSILON-5"},
	}, h.search("work"))
}

// TestPageIndexDefaultOverlayWriteThroughIndexesDefault verifies that a write
// through a project to an inherited default re-indexes the default in
// DefaultProject's catalog and never adds a duplicate tree to the project.
func TestPageIndexDefaultOverlayWriteThroughIndexesDefault(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write(files.DefaultProject, "/guide.md", "NEBULA-9")

	res := h.write("work", "/guide.md", "COMET-4")
	require.Equal(t, files.DefaultProject, res.ResolvedProject)
	require.Equal(t, "COMET-4", h.read(files.DefaultProject, "/guide.md"))
	require.Equal(t, files.HashFileContent([]byte(overlayDoc("COMET-4"))), h.tree(files.DefaultProject, "/guide.md").SourceContentHash)
	require.Empty(t, h.catalog("work"), "the project's catalog must not duplicate the default")

	want := []overlayHit{{Project: files.DefaultProject, Path: "/guide.md", Token: "COMET-4"}}
	require.Equal(t, want, h.search("work"))
	require.Equal(t, want, h.search("other"))
}

// TestPageIndexDefaultOverlayDeleteLifecycle verifies that creating and
// deleting an override changes only the project's own hit and tree, and that a
// second delete through the project removes the default and its tree for every
// project.
func TestPageIndexDefaultOverlayDeleteLifecycle(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write(files.DefaultProject, "/guide.md", "NEBULA-9")
	defaultHit := overlayHit{Project: files.DefaultProject, Path: "/guide.md", Token: "NEBULA-9"}
	require.Equal(t, []overlayHit{defaultHit}, h.search("work"))
	defaultDocID := h.catalog(files.DefaultProject)["/guide.md"].DocID

	h.override("work", "/guide.md", "PULSAR-2")
	require.Equal(t, []overlayHit{defaultHit, {Project: "work", Path: "/guide.md", Token: "PULSAR-2"}}, h.search("work"),
		"creating an override adds only the project's own hit")
	require.Equal(t, []overlayHit{defaultHit}, h.search("other"))
	overrideDocID := h.catalog("work")["/guide.md"].DocID

	// Deleting the override resets the project to the default and drops only the
	// project's tree.
	_, err := h.plugin.Delete(h.ctx, h.auth, "work", "/guide.md", false)
	require.NoError(t, err)
	require.Equal(t, "NEBULA-9", h.read("work", "/guide.md"))
	require.Empty(t, h.catalog("work"))
	h.treeGone("work", overrideDocID)
	require.Equal(t, defaultDocID, h.catalog(files.DefaultProject)["/guide.md"].DocID)
	h.tree(files.DefaultProject, "/guide.md")
	require.Equal(t, []overlayHit{defaultHit}, h.search("work"), "deleting an override removes only the project's own hit")
	require.Equal(t, []overlayHit{defaultHit}, h.search("other"))

	// Deleting again through the project deletes the default globally.
	_, err = h.plugin.Delete(h.ctx, h.auth, "work", "/guide.md", false)
	require.NoError(t, err)
	h.readMissing(files.DefaultProject, "/guide.md")
	h.readMissing("other", "/guide.md")
	require.Empty(t, h.catalog(files.DefaultProject))
	h.treeGone(files.DefaultProject, defaultDocID)
	require.Empty(t, h.search("work"))
	require.Empty(t, h.search("other"))
	require.Empty(t, h.search(files.DefaultProject))
}

// TestPageIndexDefaultOverlayCrossProjectMoveOfInheritedDefault verifies that
// moving an inherited default into another project moves the physical row and
// its tree out of DefaultProject's catalog into the target's, and that a move
// onto a path the target only inherits creates the target's own override.
func TestPageIndexDefaultOverlayCrossProjectMoveOfInheritedDefault(t *testing.T) {
	h := newDefaultOverlayHarness(t)
	h.write(files.DefaultProject, "/guide.md", "NEBULA-9")
	defaultDocID := h.catalog(files.DefaultProject)["/guide.md"].DocID
	h.write("work", "/notes.md", "RIGEL-8") // the project's own file, written before any default
	h.write(files.DefaultProject, "/notes.md", "LYRA-6")

	res, err := h.plugin.RenameAcrossProjects(h.ctx, h.auth, "work", "/guide.md", "archive", "/guide.md", false)
	require.NoError(t, err)
	require.Equal(t, 1, res.MovedCount)
	require.Equal(t, "NEBULA-9", h.read("archive", "/guide.md"))
	h.readMissing(files.DefaultProject, "/guide.md")
	h.readMissing("work", "/guide.md")
	h.readMissing("other", "/guide.md")
	require.NotContains(t, h.catalog(files.DefaultProject), "/guide.md")
	h.treeGone(files.DefaultProject, defaultDocID)
	require.Equal(t, docIDFromAuth(h.auth, "archive", "/guide.md"), h.catalog("archive")["/guide.md"].DocID)
	require.Equal(t, files.HashFileContent([]byte(overlayDoc("NEBULA-9"))), h.tree("archive", "/guide.md").SourceContentHash)

	// The target inherits /notes.md from DefaultProject; that is no collision.
	_, err = h.plugin.RenameAcrossProjects(h.ctx, h.auth, "work", "/notes.md", "archive", "/notes.md", false)
	require.NoError(t, err)
	require.Equal(t, "RIGEL-8", h.read("archive", "/notes.md"))
	require.Equal(t, "LYRA-6", h.read(files.DefaultProject, "/notes.md"))
	require.Equal(t, "LYRA-6", h.read("work", "/notes.md"), "the source now inherits the default")
	require.Empty(t, h.catalog("work"))
	require.Contains(t, h.catalog(files.DefaultProject), "/notes.md")
	require.Contains(t, h.catalog("archive"), "/notes.md")

	archive := h.search("archive")
	require.Equal(t, []overlayHit{
		{Project: files.DefaultProject, Path: "/notes.md", Token: "LYRA-6"},
		{Project: "archive", Path: "/guide.md", Token: "NEBULA-9"},
		{Project: "archive", Path: "/notes.md", Token: "RIGEL-8"},
	}, archive)
	h.requireHitsReadable("archive", archive)
	notes := []overlayHit{{Project: files.DefaultProject, Path: "/notes.md", Token: "LYRA-6"}}
	require.Equal(t, notes, h.search("other"), "the moved default left DefaultProject for every project")
	require.Equal(t, notes, h.search("work"))
}
