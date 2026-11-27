package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// overlayToolHarness drives the public MCP FileIO tools, composed over the
// production plugin manager and the rag plugin, against one real SQLite-backed
// service shared by a tenant and a foreign tenant.
type overlayToolHarness struct {
	t       *testing.T
	svc     *files.Service
	tenant  context.Context
	foreign context.Context

	writeTool, readTool, statTool, listTool, renameTool, deleteTool, searchTool, versionsTool Tool
}

// overlayToolHit is one file_search hit. Project is empty exactly when the
// returned chunk carries no "project" key; a present key is never empty.
type overlayToolHit struct {
	Project string
	Path    string
	Content string
}

// newOverlayToolHarness builds every FileIO tool the way the MCP server wires them.
func newOverlayToolHarness(t *testing.T) *overlayToolHarness {
	t.Helper()
	svc := newE2EFileService(t, false)
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, mustE2EPlugin(t, svc))
	require.NoError(t, err)
	tool := func(created Tool, err error) Tool {
		t.Helper()
		require.NoError(t, err)
		return created
	}
	h := &overlayToolHarness{
		t: t, svc: svc,
		tenant:     authtest.WithVerified(context.Background(), "tenant-key"),
		foreign:    authtest.WithVerified(context.Background(), "foreign-key"),
		writeTool:  tool(NewFileWriteTool(manager)),
		readTool:   tool(NewFileReadTool(manager)),
		statTool:   tool(NewFileStatTool(manager)),
		listTool:   tool(NewFileListTool(manager)),
		renameTool: tool(NewFileRenameTool(manager)),
		deleteTool: tool(NewFileDeleteTool(manager)),
		searchTool: tool(NewFileSearchTool(manager)),
	}
	history, err := NewFileHistoryTools(manager, svc)
	require.NoError(t, err)
	for _, candidate := range history {
		if candidate.Definition().Name == FileListVersionsToolName {
			h.versionsTool = candidate
		}
	}
	require.NotNil(t, h.versionsTool)
	return h
}

// call runs one tool call that must succeed and returns its JSON payload.
func (h *overlayToolHarness) call(ctx context.Context, target Tool, args map[string]any) map[string]any {
	h.t.Helper()
	resp, err := target.Handle(ctx, newToolReq(args))
	require.NoError(h.t, err)
	require.False(h.t, resp.IsError, "%v", resp.Content)
	return decodeToolPayload(h.t, resp)
}

// fail runs one tool call that must be refused and returns its error code.
func (h *overlayToolHarness) fail(ctx context.Context, target Tool, args map[string]any) string {
	h.t.Helper()
	resp, err := target.Handle(ctx, newToolReq(args))
	require.NoError(h.t, err)
	require.True(h.t, resp.IsError, "expected a refusal: %v", resp.Content)
	code, _ := decodeToolPayload(h.t, resp)["code"].(string)
	return code
}

// create writes a new file and returns its version.
func (h *overlayToolHarness) create(ctx context.Context, project, path, content string) string {
	h.t.Helper()
	written := h.call(ctx, h.writeTool, map[string]any{"project": project, "path": path, "content": content, "mode": "TRUNCATE", "create_only": true})
	return written["version"].(string)
}

// update replaces the file the project shows at path and returns the new version.
func (h *overlayToolHarness) update(ctx context.Context, project, path, content, expected string) string {
	h.t.Helper()
	written := h.call(ctx, h.writeTool, map[string]any{"project": project, "path": path, "content": content, "mode": "TRUNCATE", "expected_version": expected})
	return written["version"].(string)
}

// read returns the file_read payload of the file the project shows at path.
func (h *overlayToolHarness) read(ctx context.Context, project, path string) map[string]any {
	h.t.Helper()
	return h.call(ctx, h.readTool, map[string]any{"project": project, "path": path})
}

// content returns what file_read shows at path, or the refusal code.
func (h *overlayToolHarness) content(ctx context.Context, project, path string) string {
	h.t.Helper()
	resp, err := h.readTool.Handle(ctx, newToolReq(map[string]any{"project": project, "path": path}))
	require.NoError(h.t, err)
	payload := decodeToolPayload(h.t, resp)
	if resp.IsError {
		return payload["code"].(string)
	}
	return payload["content"].(string)
}

// override gives project its own file at path over an inherited default: the
// documented flow writes a draft and renames it over the default it sees.
func (h *overlayToolHarness) override(ctx context.Context, project, path, content string) {
	h.t.Helper()
	draft := h.create(ctx, project, path+".draft", content)
	inherited := h.read(ctx, project, path)
	moved := h.call(ctx, h.renameTool, map[string]any{
		"project": project, "from_path": path + ".draft", "to_path": path, "overwrite": true,
		"expected_version": draft, "expected_destination_version": inherited["version"],
	})
	require.Equal(h.t, 1, asInt(h.t, moved["moved_count"]))
}

// drainIndex runs the production index worker until queued jobs are processed.
func (h *overlayToolHarness) drainIndex() {
	h.t.Helper()
	worker := h.svc.NewIndexWorker()
	for range 5 {
		require.NoError(h.t, worker.RunOnce(context.Background()))
	}
}

// search runs file_search and returns its hits plus the raw JSON text the
// client receives. Every hit must cover its whole (small) file, so reading it
// can be compared with the chunk byte for byte.
func (h *overlayToolHarness) search(ctx context.Context, project string) ([]overlayToolHit, string) {
	h.t.Helper()
	resp, err := h.searchTool.Handle(ctx, newToolReq(map[string]any{"project": project, "query": "needle", "limit": 50}))
	require.NoError(h.t, err)
	require.False(h.t, resp.IsError, "%v", resp.Content)
	raw := behaviorTextContent(h.t, resp)
	var envelope struct {
		Chunks []map[string]any `json:"chunks"`
	}
	require.NoError(h.t, json.Unmarshal([]byte(raw), &envelope))
	hits := make([]overlayToolHit, 0, len(envelope.Chunks))
	for _, chunk := range envelope.Chunks {
		hit := overlayToolHit{Path: chunk["file_path"].(string), Content: chunk["chunk_content"].(string)}
		require.Equal(h.t, true, chunk["is_full_file"], "fixture files fit one chunk")
		if value, present := chunk["project"]; present {
			hit.Project, _ = value.(string)
			require.NotEmpty(h.t, hit.Project, "a present project label names a project")
		}
		hits = append(hits, hit)
	}
	return hits, raw
}

// historyIDs lists every history snapshot ID file_list_versions shows at path.
func (h *overlayToolHarness) historyIDs(ctx context.Context, project, path string) []string {
	h.t.Helper()
	page := h.call(ctx, h.versionsTool, map[string]any{"project": project, "path": path, "limit": 200})
	versions, _ := page["versions"].([]any)
	ids := make([]string, 0, len(versions))
	for _, entry := range versions {
		ids = append(ids, entry.(map[string]any)["id"].(string))
	}
	return ids
}

// overlayHitSet keys each hit by "project:path"; an unlabelled hit keys as ":path".
func overlayHitSet(t *testing.T, hits []overlayToolHit) map[string]string {
	t.Helper()
	set := make(map[string]string, len(hits))
	for _, hit := range hits {
		key := hit.Project + ":" + hit.Path
		_, duplicate := set[key]
		require.False(t, duplicate, "one document is returned once: %s", key)
		set[key] = hit.Content
	}
	return set
}

// TestFileToolsDefaultProjectOverlay drives the public MCP tools, with their
// mandatory version conditions, through the @DEFAULT lifecycle documented in
// docs/manual/mcp_files.md: seed a default, edit it from a project, override it
// for one project, reset the override, and keep tenants isolated.
func TestFileToolsDefaultProjectOverlay(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx, foreign := h.tenant, h.foreign

	// Seed a default; every project of the API key inherits it.
	h.create(ctx, files.DefaultProject, "users.md", "prefers tabs")
	graphql := h.read(ctx, "graphql", "users.md")
	require.Equal(t, "prefers tabs", graphql["content"])
	require.Equal(t, "prefers tabs", h.content(ctx, "web", "users.md"))

	// Editing it from any project edits the shared default.
	h.update(ctx, "graphql", "users.md", "prefers tabs; answers in English", graphql["version"].(string))
	web := h.read(ctx, "web", "users.md")
	require.Equal(t, "prefers tabs; answers in English", web["content"])
	require.Equal(t, web["content"], h.content(ctx, files.DefaultProject, "users.md"))

	// Override it for one project: create the project's copy under another name,
	// then rename it over the default with overwrite. The default stays global.
	draft := h.create(ctx, "web", "users.md.web", "prefers spaces")
	h.call(ctx, h.renameTool, map[string]any{
		"project": "web", "from_path": "users.md.web", "to_path": "users.md", "overwrite": true,
		"expected_version": draft, "expected_destination_version": web["version"],
	})
	override := h.read(ctx, "web", "users.md")
	require.Equal(t, "prefers spaces", override["content"])
	require.Equal(t, "prefers tabs; answers in English", h.content(ctx, "graphql", "users.md"))
	listed := h.call(ctx, h.listTool, map[string]any{"project": "web", "path": "/", "depth": 1})
	require.Len(t, listed["entries"], 1, "the override replaces the default name instead of duplicating it")

	// Deleting the override resets the project to the default.
	h.call(ctx, h.deleteTool, map[string]any{"project": "web", "path": "users.md", "expected_version": override["version"]})
	reset := h.read(ctx, "web", "users.md")
	require.Equal(t, "prefers tabs; answers in English", reset["content"])
	stat := h.call(ctx, h.statTool, map[string]any{"project": "web", "path": "users.md"})
	require.Equal(t, reset["version"], stat["version"])

	// Only the exact reserved name is accepted, and another key sees nothing.
	require.Equal(t, string(files.ErrCodeInvalidPath), h.fail(ctx, h.readTool, map[string]any{"project": "@default", "path": "users.md"}))
	require.Equal(t, string(files.ErrCodeNotFound), h.fail(foreign, h.readTool, map[string]any{"project": "graphql", "path": "users.md"}))
	foreignList := h.call(foreign, h.listTool, map[string]any{"project": files.DefaultProject, "path": "/", "depth": 1})
	require.Empty(t, foreignList["entries"])
}

// TestFileToolsDefaultProjectSearchLabelsEveryHit drives file_search through
// the MCP tool: an ordinary project searches itself and @DEFAULT as two
// physical projects, including the default it overrides, and every hit names
// the project to read it with; a search of @DEFAULT itself names none.
func TestFileToolsDefaultProjectSearchLabelsEveryHit(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx, foreign := h.tenant, h.foreign
	h.create(ctx, files.DefaultProject, "/notes/shared.md", "needle default shared")
	h.create(ctx, files.DefaultProject, "/notes/only.md", "needle default only")
	h.override(ctx, "alpha", "/notes/shared.md", "needle alpha override")
	h.create(ctx, "alpha", "/notes/alpha.md", "needle alpha own")
	h.create(foreign, files.DefaultProject, "/notes/foreign.md", "needle foreign default")
	h.drainIndex()

	alpha, _ := h.search(ctx, "alpha")
	require.Equal(t, map[string]string{
		"alpha:/notes/shared.md":    "needle alpha override",
		"@DEFAULT:/notes/shared.md": "needle default shared",
		"@DEFAULT:/notes/only.md":   "needle default only",
		"alpha:/notes/alpha.md":     "needle alpha own",
	}, overlayHitSet(t, alpha), "both same-path documents are hits, each labelled with its own project")

	// Reading a hit with its project returns exactly that document, also the
	// default alpha's own filesystem view hides.
	for _, hit := range alpha {
		require.Equal(t, hit.Content, h.content(ctx, hit.Project, hit.Path), "%s:%s", hit.Project, hit.Path)
	}
	require.Equal(t, "needle alpha override", h.content(ctx, "alpha", "/notes/shared.md"))
	require.Equal(t, "needle default shared", h.content(ctx, files.DefaultProject, "/notes/shared.md"))

	// A search of @DEFAULT itself spans one project: the JSON has no project key.
	defaults, raw := h.search(ctx, files.DefaultProject)
	require.Equal(t, map[string]string{
		":/notes/shared.md": "needle default shared",
		":/notes/only.md":   "needle default only",
	}, overlayHitSet(t, defaults))
	require.NotContains(t, raw, `"project"`)

	// A project without files of its own still labels the inherited hits.
	beta, _ := h.search(ctx, "beta")
	require.Equal(t, map[string]string{
		"@DEFAULT:/notes/shared.md": "needle default shared",
		"@DEFAULT:/notes/only.md":   "needle default only",
	}, overlayHitSet(t, beta))

	// The wildcard is unchanged: every physical project, every hit labelled.
	everything, _ := h.search(ctx, files.ProjectWildcard)
	require.Equal(t, overlayHitSet(t, alpha), overlayHitSet(t, everything))

	// Tenants stay isolated in both directions.
	foreignHits, _ := h.search(foreign, "alpha")
	require.Equal(t, map[string]string{"@DEFAULT:/notes/foreign.md": "needle foreign default"}, overlayHitSet(t, foreignHits))
	foreignDefaults, _ := h.search(foreign, files.DefaultProject)
	require.Equal(t, map[string]string{":/notes/foreign.md": "needle foreign default"}, overlayHitSet(t, foreignDefaults))
}

// TestFileToolsDefaultProjectCrossProjectMove moves inherited defaults with
// file_rename target_project. The source is the file the project shows; the
// destination is the target's own files, so a default at to_path is no
// collision. Moving the default removes it from @DEFAULT for every project and
// carries its history and index into the target.
func TestFileToolsDefaultProjectCrossProjectMove(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx := h.tenant
	first := h.create(ctx, files.DefaultProject, "/plan.md", "needle plan v1")
	h.update(ctx, files.DefaultProject, "/plan.md", "needle plan v2", first)
	h.create(ctx, files.DefaultProject, "/ref.md", "needle default ref")
	planHistory := h.historyIDs(ctx, files.DefaultProject, "/plan.md")
	require.NotEmpty(t, planHistory)

	// The mandatory source version comes from file_read in the source project.
	source := h.read(ctx, "alpha", "/plan.md")
	require.Equal(t, "needle plan v2", source["content"])
	require.Equal(t, "needle default ref", h.content(ctx, "archive", "/ref.md"), "archive shows the default at the destination")
	moved := h.call(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "plan.md", "to_path": "ref.md", targetProjectKey: "archive",
		"expected_version": source["version"], "overwrite": true, "destination_must_not_exist": true,
	})
	require.Equal(t, 1, asInt(t, moved["moved_count"]))

	// The default itself moved: it is gone for every project, including a third one.
	for _, project := range []string{"alpha", "beta", "archive", files.DefaultProject} {
		require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, project, "/plan.md"), project)
	}
	// archive shows its own moved file over the default; the default stays for others.
	arrived := h.read(ctx, "archive", "/ref.md")
	require.Equal(t, "needle plan v2", arrived["content"])
	require.NotEqual(t, source["version"], arrived["version"], "the move advanced the revision")
	for _, project := range []string{"alpha", "beta", files.DefaultProject} {
		require.Equal(t, "needle default ref", h.content(ctx, project, "/ref.md"), project)
	}
	listed := h.call(ctx, h.listTool, map[string]any{"project": "archive", "path": "/", "depth": 1})
	require.Len(t, listed["entries"], 1, "archive's own file replaces the default name instead of duplicating it")

	// History moved with the file identity.
	require.Empty(t, h.historyIDs(ctx, files.DefaultProject, "/plan.md"))
	require.Empty(t, h.historyIDs(ctx, "beta", "/plan.md"))
	require.Subset(t, h.historyIDs(ctx, "archive", "/ref.md"), planHistory)

	// The index moved too: the default is no longer a hit of any other project.
	h.drainIndex()
	beta, _ := h.search(ctx, "beta")
	require.Equal(t, map[string]string{"@DEFAULT:/ref.md": "needle default ref"}, overlayHitSet(t, beta))
	archive, _ := h.search(ctx, "archive")
	require.Equal(t, map[string]string{"archive:/ref.md": "needle plan v2", "@DEFAULT:/ref.md": "needle default ref"}, overlayHitSet(t, archive))

	// The version read before the move is stale now.
	require.Equal(t, string(files.ErrCodeVersionConflict), h.fail(ctx, h.renameTool, map[string]any{
		"project": "archive", "from_path": "/ref.md", "to_path": "/plan.md", targetProjectKey: "alpha", "expected_version": source["version"],
	}))

	// Moving an inherited default to its own path in a project that shows it
	// needs no overwrite: the target holds no file of its own there.
	inherited := h.read(ctx, "alpha", "/ref.md")
	moved = h.call(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "/ref.md", "to_path": "/ref.md", targetProjectKey: "beta", "expected_version": inherited["version"],
	})
	require.Equal(t, 1, asInt(t, moved["moved_count"]))
	require.Equal(t, "needle default ref", h.content(ctx, "beta", "/ref.md"))
	require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, "alpha", "/ref.md"))
	require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, files.DefaultProject, "/ref.md"))
	require.Equal(t, "needle plan v2", h.content(ctx, "archive", "/ref.md"))
}

// TestFileToolsDefaultProjectMoveIntoDefault moves a project file into the
// shared layer with target_project="@DEFAULT", whose own files are the
// destination: an existing default there is a collision.
func TestFileToolsDefaultProjectMoveIntoDefault(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx := h.tenant
	h.create(ctx, files.DefaultProject, "/taken.md", "needle default taken")
	mine := h.create(ctx, "alpha", "/mine.md", "needle alpha mine")
	require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, "beta", "/mine.md"))

	require.Equal(t, string(files.ErrCodeVersionConflict), h.fail(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "/mine.md", "to_path": "/taken.md", targetProjectKey: files.DefaultProject,
		"expected_version": mine, "destination_must_not_exist": true,
	}), "the destination condition is judged on @DEFAULT's own files")
	require.Equal(t, "needle alpha mine", h.content(ctx, "alpha", "/mine.md"))
	require.Equal(t, "needle default taken", h.content(ctx, "beta", "/taken.md"))

	moved := h.call(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "/mine.md", "to_path": "/shared/mine.md", targetProjectKey: files.DefaultProject, "expected_version": mine,
	})
	require.Equal(t, 1, asInt(t, moved["moved_count"]))
	require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, "alpha", "/mine.md"))
	for _, project := range []string{"alpha", "beta", files.DefaultProject} {
		require.Equal(t, "needle alpha mine", h.content(ctx, project, "/shared/mine.md"), project)
	}

	// The moved file is an ordinary default now: an edit from any project writes through.
	shared := h.read(ctx, "beta", "/shared/mine.md")
	h.update(ctx, "beta", "/shared/mine.md", "needle shared by all", shared["version"].(string))
	require.Equal(t, "needle shared by all", h.content(ctx, "alpha", "/shared/mine.md"))

	h.drainIndex()
	defaults, raw := h.search(ctx, files.DefaultProject)
	require.Equal(t, map[string]string{":/shared/mine.md": "needle shared by all", ":/taken.md": "needle default taken"}, overlayHitSet(t, defaults))
	require.NotContains(t, raw, `"project"`)
	alpha, _ := h.search(ctx, "alpha")
	require.Equal(t, map[string]string{"@DEFAULT:/shared/mine.md": "needle shared by all", "@DEFAULT:/taken.md": "needle default taken"}, overlayHitSet(t, alpha))
}

// TestFileToolsDefaultProjectRenameRefusesHiddenDefaultDestination pins the
// same-project overwrite rule: renaming an inherited default onto a path where
// the project has its own file over a hidden default is refused, because the
// moved default would land under a default the caller never saw.
func TestFileToolsDefaultProjectRenameRefusesHiddenDefaultDestination(t *testing.T) {
	h := newOverlayToolHarness(t)
	ctx := h.tenant
	h.create(ctx, files.DefaultProject, "/src.md", "needle default src")
	h.create(ctx, files.DefaultProject, "/dst.md", "needle default dst")
	h.override(ctx, "alpha", "/dst.md", "needle alpha dst")
	source := h.read(ctx, "alpha", "/src.md")
	destination := h.read(ctx, "alpha", "/dst.md")

	require.Equal(t, string(files.ErrCodeAlreadyExists), h.fail(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "/src.md", "to_path": "/dst.md", "overwrite": true,
		"expected_version": source["version"], "expected_destination_version": destination["version"],
	}))
	require.Equal(t, "needle default src", h.content(ctx, "alpha", "/src.md"))
	require.Equal(t, "needle alpha dst", h.content(ctx, "alpha", "/dst.md"))
	require.Equal(t, "needle default src", h.content(ctx, "beta", "/src.md"))
	require.Equal(t, "needle default dst", h.content(ctx, "beta", "/dst.md"))

	// Without a hidden default the same overwrite replaces the project file, and
	// the renamed default is what every project then shows.
	own := h.create(ctx, "alpha", "/own.md", "needle alpha own")
	moved := h.call(ctx, h.renameTool, map[string]any{
		"project": "alpha", "from_path": "/src.md", "to_path": "/own.md", "overwrite": true,
		"expected_version": source["version"], "expected_destination_version": own,
	})
	require.Equal(t, 1, asInt(t, moved["moved_count"]))
	require.Equal(t, "needle default src", h.content(ctx, "alpha", "/own.md"))
	require.Equal(t, "needle default src", h.content(ctx, "beta", "/own.md"))
	require.Equal(t, string(files.ErrCodeNotFound), h.content(ctx, "beta", "/src.md"))
}
