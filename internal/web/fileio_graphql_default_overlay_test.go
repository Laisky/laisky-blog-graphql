package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/mark3labs/mcp-go/mcp"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	mcptools "github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// overlayEmbedder returns one constant vector, so semantic retrieval serves
// every indexed chunk in the searched projects and hit sets are deterministic.
type overlayEmbedder struct{}

// EmbedTexts returns a constant vector per input.
func (overlayEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		vectors = append(vectors, pgvector.NewVector([]float32{1, 0}))
	}
	return vectors, nil
}

// overlayCredentialStore keeps encrypted credential envelopes in memory.
type overlayCredentialStore struct {
	mu   sync.Mutex
	data map[string]string
}

// Store saves one envelope.
func (s *overlayCredentialStore) Store(_ context.Context, key, payload string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]string{}
	}
	s.data[key] = payload
	return nil
}

// Load returns one envelope or sql.ErrNoRows.
func (s *overlayCredentialStore) Load(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	if !ok {
		return "", sql.ErrNoRows
	}
	return value, nil
}

// Delete removes one envelope.
func (s *overlayCredentialStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// overlayStack is one real FileIO service behind both public interfaces, wired
// as cmd/api.go wires them: the plugin manager over the rag plugin serves the
// GraphQL FileIO resolvers and the MCP tools, and the service serves history.
type overlayStack struct {
	t       *testing.T
	svc     *files.Service
	manager *mcpplugin.Manager
	server  http.Handler
}

// newOverlayStack builds the stack over an in-memory SQLite database.
func newOverlayStack(t *testing.T) *overlayStack {
	t.Helper()
	settings := files.LoadSettingsFromConfig()
	settings.Search.Enabled = true
	settings.MaxProjectBytes = 1_000_000
	settings.Index.BatchSize = 100
	settings.Index.ChunkBytes = 64
	settings.Security.EncryptionKEKs = map[uint16]string{1: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))}

	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", name, time.Now().UTC().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	credential, err := files.NewCredentialProtector(settings.Security)
	require.NoError(t, err)
	svc, err := files.NewService(db, settings, overlayEmbedder{}, nil, credential, nil, nil, func() time.Time { return time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC) })
	require.NoError(t, err)
	plugin, err := ragplugin.New(svc)
	require.NoError(t, err)
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, plugin)
	require.NoError(t, err)
	resolver := newContractResolver(ResolverArgs{MCPFileService: manager, FilesService: svc})
	return &overlayStack{t: t, svc: svc, manager: manager, server: handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: resolver}))}
}

// drainIndex runs the production index worker until queued jobs are processed.
func (s *overlayStack) drainIndex() {
	s.t.Helper()
	worker := s.svc.NewIndexWorker()
	for range 5 {
		require.NoError(s.t, worker.RunOnce(context.Background()))
	}
}

// overlayRead is one read: content and version, or the refusal code.
type overlayRead struct {
	Content, Version, Code string
}

// overlayHit is one search hit. Project is empty exactly when the interface
// returned no project for it: an absent MCP key or a GraphQL null.
type overlayHit struct {
	Project, Path, Content string
}

// overlayRename is one rename request; an empty Target renames within Project.
type overlayRename struct {
	Project, From, To, Target     string
	Overwrite, MustNotExist       bool
	Expected, ExpectedDestination string
}

// overlayClient is one public FileIO interface acting for one API key.
type overlayClient interface {
	create(project, path, content string) string
	update(project, path, content, expected string) string
	read(project, path string) overlayRead
	// rename returns "moved=N" or the refusal code.
	rename(request overlayRename) string
	search(project string) []overlayHit
	history(project, path string) []string
}

// mcpOverlayClient drives the MCP FileIO tools.
type mcpOverlayClient struct {
	t                                                         *testing.T
	ctx                                                       context.Context
	writeTool, readTool, renameTool, searchTool, versionsTool mcptools.Tool
}

// mcp returns an MCP client acting for apiKey.
func (s *overlayStack) mcp(apiKey string) *mcpOverlayClient {
	s.t.Helper()
	tool := func(created mcptools.Tool, err error) mcptools.Tool {
		s.t.Helper()
		require.NoError(s.t, err)
		return created
	}
	client := &mcpOverlayClient{
		t: s.t, ctx: authtest.WithVerified(context.Background(), apiKey),
		writeTool: tool(mcptools.NewFileWriteTool(s.manager)), readTool: tool(mcptools.NewFileReadTool(s.manager)),
		renameTool: tool(mcptools.NewFileRenameTool(s.manager)), searchTool: tool(mcptools.NewFileSearchTool(s.manager)),
	}
	history, err := mcptools.NewFileHistoryTools(s.manager, s.svc)
	require.NoError(s.t, err)
	for _, candidate := range history {
		if candidate.Definition().Name == mcptools.FileListVersionsToolName {
			client.versionsTool = candidate
		}
	}
	require.NotNil(s.t, client.versionsTool)
	return client
}

// call runs one tool and returns its JSON text, its decoded payload and whether it was refused.
func (c *mcpOverlayClient) call(target mcptools.Tool, args map[string]any) (string, map[string]any, bool) {
	c.t.Helper()
	result, err := target.Handle(c.ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	require.NoError(c.t, err)
	require.NotEmpty(c.t, result.Content)
	text, ok := mcp.AsTextContent(result.Content[0])
	require.True(c.t, ok)
	payload := map[string]any{}
	require.NoError(c.t, json.Unmarshal([]byte(text.Text), &payload), text.Text)
	return text.Text, payload, result.IsError
}

// ok runs one tool call that must succeed.
func (c *mcpOverlayClient) ok(target mcptools.Tool, args map[string]any) map[string]any {
	c.t.Helper()
	text, payload, refused := c.call(target, args)
	require.False(c.t, refused, text)
	return payload
}

func (c *mcpOverlayClient) create(project, path, content string) string {
	c.t.Helper()
	return c.ok(c.writeTool, map[string]any{"project": project, "path": path, "content": content, "mode": "TRUNCATE", "create_only": true})["version"].(string)
}

func (c *mcpOverlayClient) update(project, path, content, expected string) string {
	c.t.Helper()
	return c.ok(c.writeTool, map[string]any{"project": project, "path": path, "content": content, "mode": "TRUNCATE", "expected_version": expected})["version"].(string)
}

func (c *mcpOverlayClient) read(project, path string) overlayRead {
	c.t.Helper()
	_, payload, refused := c.call(c.readTool, map[string]any{"project": project, "path": path})
	if refused {
		return overlayRead{Code: payload["code"].(string)}
	}
	return overlayRead{Content: payload["content"].(string), Version: payload["version"].(string)}
}

func (c *mcpOverlayClient) rename(request overlayRename) string {
	c.t.Helper()
	args := map[string]any{"project": request.Project, "from_path": request.From, "to_path": request.To, "expected_version": request.Expected}
	if request.Target != "" {
		args["target_project"] = request.Target
	}
	if request.Overwrite {
		args["overwrite"] = true
	}
	if request.MustNotExist {
		args["destination_must_not_exist"] = true
	}
	if request.ExpectedDestination != "" {
		args["expected_destination_version"] = request.ExpectedDestination
	}
	_, payload, refused := c.call(c.renameTool, args)
	if refused {
		return payload["code"].(string)
	}
	return fmt.Sprintf("moved=%v", payload["moved_count"])
}

// searchRaw returns the hits and the JSON text an MCP client receives.
func (c *mcpOverlayClient) searchRaw(project string) ([]overlayHit, string) {
	c.t.Helper()
	text, _, refused := c.call(c.searchTool, map[string]any{"project": project, "query": "needle", "limit": 50})
	require.False(c.t, refused, text)
	var envelope struct {
		Chunks []map[string]any `json:"chunks"`
	}
	require.NoError(c.t, json.Unmarshal([]byte(text), &envelope))
	hits := make([]overlayHit, 0, len(envelope.Chunks))
	for _, chunk := range envelope.Chunks {
		require.Equal(c.t, true, chunk["is_full_file"], "fixture files fit one chunk")
		hit := overlayHit{Path: chunk["file_path"].(string), Content: chunk["chunk_content"].(string)}
		if value, present := chunk["project"]; present {
			hit.Project, _ = value.(string)
			require.NotEmpty(c.t, hit.Project, "a present project label names a project")
		}
		hits = append(hits, hit)
	}
	return hits, text
}

func (c *mcpOverlayClient) search(project string) []overlayHit {
	c.t.Helper()
	hits, _ := c.searchRaw(project)
	return hits
}

func (c *mcpOverlayClient) history(project, path string) []string {
	c.t.Helper()
	page := c.ok(c.versionsTool, map[string]any{"project": project, "path": path, "limit": 200})
	versions, _ := page["versions"].([]any)
	ids := make([]string, 0, len(versions))
	for _, entry := range versions {
		ids = append(ids, entry.(map[string]any)["id"].(string))
	}
	return ids
}

// graphQLOverlayClient drives the GraphQL FileIO resolvers over HTTP.
type graphQLOverlayClient struct {
	t      *testing.T
	server http.Handler
	apiKey string
}

// graphql returns a GraphQL client acting for apiKey.
func (s *overlayStack) graphql(apiKey string) *graphQLOverlayClient {
	return &graphQLOverlayClient{t: s.t, server: s.server, apiKey: apiKey}
}

// do executes one operation with variables against the generated schema.
func (c *graphQLOverlayClient) do(query string, variables map[string]any) graphQLResponse {
	c.t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	require.NoError(c.t, err)
	request := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(authtest.WithVerified(request.Context(), c.apiKey))
	recorder := httptest.NewRecorder()
	c.server.ServeHTTP(recorder, request)
	var response graphQLResponse
	require.NoError(c.t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

// ok executes one operation that must succeed and decodes its field into out.
func (c *graphQLOverlayClient) ok(field, query string, variables map[string]any, out any) {
	c.t.Helper()
	response := c.do(query, variables)
	require.Empty(c.t, response.Errors, "%+v", response.Errors)
	require.NoError(c.t, json.Unmarshal(response.Data[field], out))
}

func (c *graphQLOverlayClient) create(project, path, content string) string {
	c.t.Helper()
	var written struct{ Version string }
	c.ok("FileWrite", `mutation($p:String!,$path:String!,$c:String!){ FileWrite(project:$p,path:$path,content:$c,mode:TRUNCATE,create_only:true){ version } }`,
		map[string]any{"p": project, "path": path, "c": content}, &written)
	return written.Version
}

func (c *graphQLOverlayClient) update(project, path, content, expected string) string {
	c.t.Helper()
	var written struct{ Version string }
	c.ok("FileWrite", `mutation($p:String!,$path:String!,$c:String!,$v:String!){ FileWrite(project:$p,path:$path,content:$c,mode:TRUNCATE,expected_version:$v){ version } }`,
		map[string]any{"p": project, "path": path, "c": content, "v": expected}, &written)
	return written.Version
}

func (c *graphQLOverlayClient) read(project, path string) overlayRead {
	c.t.Helper()
	response := c.do(`query($p:String!,$path:String!){ FileRead(project:$p,path:$path){ content version } }`, map[string]any{"p": project, "path": path})
	if len(response.Errors) > 0 {
		return overlayRead{Code: errorCode(c.t, response)}
	}
	var read struct{ Content, Version string }
	require.NoError(c.t, json.Unmarshal(response.Data["FileRead"], &read))
	return overlayRead{Content: read.Content, Version: read.Version}
}

func (c *graphQLOverlayClient) rename(request overlayRename) string {
	c.t.Helper()
	variables := map[string]any{"p": request.Project, "from": request.From, "to": request.To, "overwrite": request.Overwrite, "v": request.Expected}
	if request.Target != "" {
		variables["target"] = request.Target
	}
	if request.MustNotExist {
		variables["dmne"] = true
	}
	if request.ExpectedDestination != "" {
		variables["dv"] = request.ExpectedDestination
	}
	response := c.do(`mutation($p:String!,$from:String!,$to:String!,$target:String,$overwrite:Boolean!,$v:String!,$dv:String,$dmne:Boolean){
		FileRename(project:$p,from_path:$from,to_path:$to,target_project:$target,overwrite:$overwrite,expected_version:$v,
			expected_destination_version:$dv,destination_must_not_exist:$dmne){ moved_count } }`, variables)
	if len(response.Errors) > 0 {
		return errorCode(c.t, response)
	}
	var moved struct {
		MovedCount int `json:"moved_count"`
	}
	require.NoError(c.t, json.Unmarshal(response.Data["FileRename"], &moved))
	return fmt.Sprintf("moved=%d", moved.MovedCount)
}

// searchRaw returns the hits and the JSON of the FileSearch field.
func (c *graphQLOverlayClient) searchRaw(project string) ([]overlayHit, string) {
	c.t.Helper()
	response := c.do(`query($p:String!){ FileSearch(project:$p,query:"needle",limit:50){ chunks { project file_path chunk_content is_full_file } } }`,
		map[string]any{"p": project})
	require.Empty(c.t, response.Errors, "%+v", response.Errors)
	var result struct {
		Chunks []struct {
			Project      *string `json:"project"`
			FilePath     string  `json:"file_path"`
			ChunkContent string  `json:"chunk_content"`
			IsFullFile   bool    `json:"is_full_file"`
		} `json:"chunks"`
	}
	require.NoError(c.t, json.Unmarshal(response.Data["FileSearch"], &result))
	hits := make([]overlayHit, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		require.True(c.t, chunk.IsFullFile, "fixture files fit one chunk")
		hit := overlayHit{Path: chunk.FilePath, Content: chunk.ChunkContent}
		if chunk.Project != nil {
			require.NotEmpty(c.t, *chunk.Project, "a non-null project label names a project")
			hit.Project = *chunk.Project
		}
		hits = append(hits, hit)
	}
	return hits, string(response.Data["FileSearch"])
}

func (c *graphQLOverlayClient) search(project string) []overlayHit {
	c.t.Helper()
	hits, _ := c.searchRaw(project)
	return hits
}

func (c *graphQLOverlayClient) history(project, path string) []string {
	c.t.Helper()
	var page struct {
		Versions []struct{ ID string } `json:"versions"`
	}
	c.ok("FileListVersions", `query($p:String!,$path:String!){ FileListVersions(project:$p,path:$path,limit:200){ versions { id } } }`,
		map[string]any{"p": project, "path": path}, &page)
	ids := make([]string, 0, len(page.Versions))
	for _, entry := range page.Versions {
		ids = append(ids, entry.ID)
	}
	return ids
}

// overlayHitKeys keys each hit by "project:path" (":path" when unlabelled).
func overlayHitKeys(t *testing.T, hits []overlayHit) map[string]string {
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

// overlayOutcome is what one interface observed while driving the scenario.
type overlayOutcome struct {
	// Searches maps "phase project" to the hit set keyed by project:path.
	Searches map[string]map[string]string
	// Renames maps a step to "moved=N" or the refusal code.
	Renames map[string]string
	// Reads maps "project:path" to the content shown there or the refusal code.
	Reads map[string]string
	// History maps "project:path" to the number of history snapshots shown.
	History map[string]int
}

// overlayScenarioPaths are read in every project at the end of the scenario.
var overlayScenarioPaths = []string{"/plan.md", "/ref.md", "/mine.md", "/shared/mine.md", "/notes/shared.md", "/notes/only.md"}

// overlayScenarioProjects are the projects whose views the scenario observes.
var overlayScenarioProjects = []string{"alpha", "beta", "archive", files.DefaultProject}

// runDefaultOverlayScenario drives the @DEFAULT search and move contract
// through one interface for one tenant and records what it observed.
func runDefaultOverlayScenario(t *testing.T, c, foreign overlayClient, drain func()) overlayOutcome {
	t.Helper()
	out := overlayOutcome{Searches: map[string]map[string]string{}, Renames: map[string]string{}, Reads: map[string]string{}, History: map[string]int{}}
	searchAll := func(phase string, projects ...string) {
		t.Helper()
		drain()
		for _, project := range projects {
			hits := c.search(project)
			out.Searches[phase+" "+project] = overlayHitKeys(t, hits)
			for _, hit := range hits {
				readProject := hit.Project
				if readProject == "" {
					readProject = project
				}
				require.Equal(t, hit.Content, c.read(readProject, hit.Path).Content, "reading %s:%s returns exactly that hit", readProject, hit.Path)
			}
		}
	}

	// Seed the shared layer, one project override, and a foreign tenant's default.
	c.create(files.DefaultProject, "/notes/shared.md", "needle default shared")
	c.create(files.DefaultProject, "/notes/only.md", "needle default only")
	first := c.create(files.DefaultProject, "/plan.md", "needle plan v1")
	c.update(files.DefaultProject, "/plan.md", "needle plan v2", first)
	c.create(files.DefaultProject, "/ref.md", "needle default ref")
	draft := c.create("alpha", "/notes/shared.md.draft", "needle alpha override")
	out.Renames["override"] = c.rename(overlayRename{Project: "alpha", From: "/notes/shared.md.draft", To: "/notes/shared.md", Overwrite: true,
		Expected: draft, ExpectedDestination: c.read("alpha", "/notes/shared.md").Version})
	foreign.create(files.DefaultProject, "/notes/foreign.md", "needle foreign default")
	searchAll("before", "alpha", files.DefaultProject, "beta")

	// Move the inherited /plan.md into archive, onto the default archive shows at /ref.md.
	planHistory := c.history(files.DefaultProject, "/plan.md")
	require.NotEmpty(t, planHistory)
	source := c.read("alpha", "/plan.md")
	out.Renames["inherited into archive over its default"] = c.rename(overlayRename{Project: "alpha", From: "/plan.md", To: "/ref.md", Target: "archive",
		Overwrite: true, MustNotExist: true, Expected: source.Version})
	arrived := c.read("archive", "/ref.md")
	require.NotEqual(t, source.Version, arrived.Version, "the move advanced the revision")
	require.Subset(t, c.history("archive", "/ref.md"), planHistory, "history moves with the file")
	out.Renames["stale source version"] = c.rename(overlayRename{Project: "archive", From: "/ref.md", To: "/plan.md", Target: "alpha", Expected: source.Version})
	inherited := c.read("alpha", "/ref.md")
	out.Renames["inherited to its own path in beta"] = c.rename(overlayRename{Project: "alpha", From: "/ref.md", To: "/ref.md", Target: "beta", Expected: inherited.Version})

	// Move a project file into the shared layer, whose own files are the destination.
	mine := c.create("alpha", "/mine.md", "needle alpha mine")
	out.Renames["into @DEFAULT onto a default"] = c.rename(overlayRename{Project: "alpha", From: "/mine.md", To: "/notes/only.md", Target: files.DefaultProject,
		MustNotExist: true, Expected: mine})
	out.Renames["into @DEFAULT"] = c.rename(overlayRename{Project: "alpha", From: "/mine.md", To: "/shared/mine.md", Target: files.DefaultProject, Expected: mine})

	// A same-project overwrite of a project file that hides a default is refused.
	out.Renames["default onto an override hiding a default"] = c.rename(overlayRename{Project: "alpha", From: "/notes/only.md", To: "/notes/shared.md",
		Overwrite: true, Expected: c.read("alpha", "/notes/only.md").Version, ExpectedDestination: c.read("alpha", "/notes/shared.md").Version})

	for _, project := range overlayScenarioProjects {
		for _, path := range overlayScenarioPaths {
			read := c.read(project, path)
			out.Reads[project+":"+path] = read.Content + read.Code
		}
		out.History[project+":/plan.md"] = len(c.history(project, "/plan.md"))
	}
	searchAll("after", "alpha", "beta", "archive", files.DefaultProject)
	out.Searches["foreign alpha"] = overlayHitKeys(t, foreign.search("alpha"))
	out.Searches["foreign "+files.DefaultProject] = overlayHitKeys(t, foreign.search(files.DefaultProject))
	foreignRead := foreign.read("alpha", "/notes/only.md")
	out.Reads["foreign alpha:/notes/only.md"] = foreignRead.Content + foreignRead.Code
	return out
}

// expectedDefaultOverlayOutcome is the documented contract for the scenario.
func expectedDefaultOverlayOutcome() overlayOutcome {
	const (
		override   = "needle alpha override"
		shared     = "needle default shared"
		only       = "needle default only"
		plan       = "needle plan v2"
		ref        = "needle default ref"
		mine       = "needle alpha mine"
		notFound   = string(files.ErrCodeNotFound)
		conflict   = string(files.ErrCodeVersionConflict)
		occupied   = string(files.ErrCodeAlreadyExists)
		movedOne   = "moved=1"
		defaultKey = files.DefaultProject + ":"
	)
	reads := map[string]string{}
	history := map[string]int{}
	for _, project := range overlayScenarioProjects {
		reads[project+":/plan.md"] = notFound // moved out of @DEFAULT, so gone for every project
		reads[project+":/ref.md"] = notFound  // the default moved into beta
		reads[project+":/mine.md"] = notFound // moved into @DEFAULT
		reads[project+":/shared/mine.md"] = mine
		reads[project+":/notes/shared.md"] = shared
		reads[project+":/notes/only.md"] = only
		history[project+":/plan.md"] = 0 // history left with the moved file
	}
	reads["alpha:/notes/shared.md"] = override
	reads["archive:/ref.md"] = plan
	reads["beta:/ref.md"] = ref
	reads["foreign alpha:/notes/only.md"] = notFound

	before := map[string]string{"alpha:/notes/shared.md": override, defaultKey + "/notes/shared.md": shared, defaultKey + "/notes/only.md": only,
		defaultKey + "/plan.md": plan, defaultKey + "/ref.md": ref}
	inheritedAfter := map[string]string{defaultKey + "/notes/shared.md": shared, defaultKey + "/notes/only.md": only, defaultKey + "/shared/mine.md": mine}
	with := func(extra map[string]string) map[string]string {
		merged := map[string]string{}
		for key, value := range inheritedAfter {
			merged[key] = value
		}
		for key, value := range extra {
			merged[key] = value
		}
		return merged
	}
	return overlayOutcome{
		Searches: map[string]map[string]string{
			"before alpha":                    before,
			"before " + files.DefaultProject:  {":/notes/shared.md": shared, ":/notes/only.md": only, ":/plan.md": plan, ":/ref.md": ref},
			"before beta":                     {defaultKey + "/notes/shared.md": shared, defaultKey + "/notes/only.md": only, defaultKey + "/plan.md": plan, defaultKey + "/ref.md": ref},
			"after alpha":                     with(map[string]string{"alpha:/notes/shared.md": override}),
			"after beta":                      with(map[string]string{"beta:/ref.md": ref}),
			"after archive":                   with(map[string]string{"archive:/ref.md": plan}),
			"after " + files.DefaultProject:   {":/notes/shared.md": shared, ":/notes/only.md": only, ":/shared/mine.md": mine},
			"foreign alpha":                   {defaultKey + "/notes/foreign.md": "needle foreign default"},
			"foreign " + files.DefaultProject: {":/notes/foreign.md": "needle foreign default"},
		},
		Renames: map[string]string{
			"override": movedOne,
			"inherited into archive over its default":   movedOne,
			"stale source version":                      conflict,
			"inherited to its own path in beta":         movedOne,
			"into @DEFAULT onto a default":              conflict,
			"into @DEFAULT":                             movedOne,
			"default onto an override hiding a default": occupied,
		},
		Reads:   reads,
		History: history,
	}
}

// TestGraphQLFileIODefaultOverlayParity drives the @DEFAULT search and
// cross-project move contract through the GraphQL FileIO resolvers and through
// the MCP tools, each for its own tenant of one real service, and requires both
// to match the documented outcome and each other. It then reads the GraphQL
// tenant's data through both interfaces and requires identical hits and reads.
func TestGraphQLFileIODefaultOverlayParity(t *testing.T) {
	stack := newOverlayStack(t)
	expected := expectedDefaultOverlayOutcome()

	viaGraphQL := runDefaultOverlayScenario(t, stack.graphql("sk-overlay-graphql"), stack.graphql("sk-overlay-graphql-foreign"), stack.drainIndex)
	require.Equal(t, expected, viaGraphQL)
	viaMCP := runDefaultOverlayScenario(t, stack.mcp("sk-overlay-mcp"), stack.mcp("sk-overlay-mcp-foreign"), stack.drainIndex)
	require.Equal(t, expected, viaMCP)
	require.Equal(t, viaMCP, viaGraphQL, "GraphQL and MCP observe the same @DEFAULT semantics")

	// The same stored data reads identically through both interfaces, down to
	// the (project, path) of every hit and every version token.
	stack.drainIndex()
	graphql, sameKeyMCP := stack.graphql("sk-overlay-graphql"), stack.mcp("sk-overlay-graphql")
	for _, project := range overlayScenarioProjects {
		require.Equal(t, overlayHitKeys(t, sameKeyMCP.search(project)), overlayHitKeys(t, graphql.search(project)), project)
		for _, path := range overlayScenarioPaths {
			require.Equal(t, sameKeyMCP.read(project, path), graphql.read(project, path), "%s:%s", project, path)
		}
	}

	// A labelled hit names its project on both wires; a search of @DEFAULT
	// itself omits the MCP key and returns a GraphQL null.
	_, mcpLabelled := sameKeyMCP.searchRaw("alpha")
	_, graphQLLabelled := graphql.searchRaw("alpha")
	require.Contains(t, mcpLabelled, `"project":"@DEFAULT"`)
	require.Contains(t, graphQLLabelled, `"project":"@DEFAULT"`)
	require.Contains(t, graphQLLabelled, `"project":"alpha"`)
	_, mcpDefaults := sameKeyMCP.searchRaw(files.DefaultProject)
	require.NotContains(t, mcpDefaults, `"project"`)
	defaultHits, graphQLDefaults := graphql.searchRaw(files.DefaultProject)
	require.Len(t, defaultHits, 3)
	require.Equal(t, len(defaultHits), strings.Count(graphQLDefaults, `"project":null`))
}

// TestGraphQLFileIODefaultOverlayWildcardSearchParity requires GraphQL
// FileSearch(project:"*") to search every project of the caller with every hit
// labelled, as MCP file_search does for the same data and as the FileIOChunk
// schema comment documents ("*" searches every project).
func TestGraphQLFileIODefaultOverlayWildcardSearchParity(t *testing.T) {
	stack := newOverlayStack(t)
	graphql, sameKeyMCP := stack.graphql("sk-overlay-wildcard"), stack.mcp("sk-overlay-wildcard")
	graphql.create(files.DefaultProject, "/notes/shared.md", "needle default shared")
	graphql.create("alpha", "/notes/alpha.md", "needle alpha own")
	stack.drainIndex()

	viaMCP := overlayHitKeys(t, sameKeyMCP.search(files.ProjectWildcard))
	require.Equal(t, map[string]string{
		files.DefaultProject + ":/notes/shared.md": "needle default shared",
		"alpha:/notes/alpha.md":                    "needle alpha own",
	}, viaMCP)
	response := graphql.do(`query{ FileSearch(project:"*",query:"needle",limit:50){ chunks { project file_path } } }`, nil)
	require.Empty(t, response.Errors, "GraphQL must accept the wildcard MCP accepts")
	require.Equal(t, viaMCP, overlayHitKeys(t, graphql.search(files.ProjectWildcard)))
}

// TestGraphQLFileIODefaultHistoryIsPerFile requires GraphQL history to be the
// history of the file a path resolves to, as MCP shows it for the same data: an
// override and the default it hides keep separate histories, and a history ID
// of the other layer is NOT_FOUND for read and restore, without effect.
func TestGraphQLFileIODefaultHistoryIsPerFile(t *testing.T) {
	stack := newOverlayStack(t)
	graphql, sameKeyMCP := stack.graphql("sk-overlay-history"), stack.mcp("sk-overlay-history")
	first := graphql.create(files.DefaultProject, "/h.md", "needle h default v1")
	graphql.update(files.DefaultProject, "/h.md", "needle h default v2", first)
	draft := graphql.create("alpha", "/h.md.draft", "needle h alpha v1")
	require.Equal(t, "moved=1", graphql.rename(overlayRename{Project: "alpha", From: "/h.md.draft", To: "/h.md", Overwrite: true,
		Expected: draft, ExpectedDestination: graphql.read("alpha", "/h.md").Version}))
	graphql.update("alpha", "/h.md", "needle h alpha v2", graphql.read("alpha", "/h.md").Version)

	alphaIDs, defaultIDs := graphql.history("alpha", "/h.md"), graphql.history(files.DefaultProject, "/h.md")
	require.NotEmpty(t, alphaIDs)
	require.NotEmpty(t, defaultIDs)
	for _, id := range alphaIDs {
		require.NotContains(t, defaultIDs, id, "two independent /h.md files never share one list")
	}
	require.Equal(t, defaultIDs, graphql.history("beta", "/h.md"), "beta resolves to the default")
	for _, project := range []string{"alpha", "beta", files.DefaultProject} {
		require.Equal(t, sameKeyMCP.history(project, "/h.md"), graphql.history(project, "/h.md"), project)
	}

	readVersion := func(project, id string) graphQLResponse {
		return graphql.do(`query($p:String!,$path:String!,$id:String!){ FileReadVersion(project:$p,path:$path,history_id:$id){ content } }`,
			map[string]any{"p": project, "path": "/h.md", "id": id})
	}
	restore := func(project, id, expected string) graphQLResponse {
		return graphql.do(`mutation($p:String!,$path:String!,$id:String!,$v:String){
			FileRestoreVersion(project:$p,path:$path,history_id:$id,expected_version:$v){ version } }`,
			map[string]any{"p": project, "path": "/h.md", "id": id, "v": expected})
	}
	notFound := string(files.ErrCodeNotFound)
	alpha, inherited := graphql.read("alpha", "/h.md"), graphql.read("beta", "/h.md")
	require.Equal(t, notFound, errorCode(t, readVersion("alpha", defaultIDs[0])))
	require.Equal(t, notFound, errorCode(t, readVersion("beta", alphaIDs[0])))
	require.Equal(t, notFound, errorCode(t, readVersion(files.DefaultProject, alphaIDs[0])))
	require.Equal(t, notFound, errorCode(t, restore("alpha", defaultIDs[0], alpha.Version)))
	require.Equal(t, notFound, errorCode(t, restore("beta", alphaIDs[0], inherited.Version)))
	require.Equal(t, alpha, graphql.read("alpha", "/h.md"), "a refused restore changes nothing")
	require.Equal(t, inherited, graphql.read(files.DefaultProject, "/h.md"))
	require.Equal(t, alphaIDs, graphql.history("alpha", "/h.md"))
	require.Equal(t, defaultIDs, graphql.history(files.DefaultProject, "/h.md"))

	// The override's own snapshot restores into the override only.
	response := restore("alpha", alphaIDs[0], alpha.Version)
	require.Empty(t, response.Errors, "%+v", response.Errors)
	require.Equal(t, "needle h alpha v1", graphql.read("alpha", "/h.md").Content)
	require.Equal(t, "needle h default v2", graphql.read("beta", "/h.md").Content)
}
