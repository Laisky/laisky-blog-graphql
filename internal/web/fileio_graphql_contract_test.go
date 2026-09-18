package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	mcptools "github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// recordingPlugin is a real mcpplugin.Plugin implementation backed by memory.
// It is NOT a stub of the contract under test: the version preconditions are
// enforced by the shared files package through the request context, and this
// plugin only reports what the shared gate actually let through.
type recordingPlugin struct {
	mu      sync.Mutex
	version string
	content string
	// attempts counts every mutating call that REACHED this backend, whether or
	// not its precondition then passed. A contract test must separate "rejected
	// before storage" from "rejected by storage", otherwise the fixture's own
	// check would mask a resolver that skipped the shared gate entirely.
	attempts int
	writes   int
	deletes  int
	renames  int
	reads    int
	plugins  []string
}

func newRecordingPlugin() *recordingPlugin {
	return &recordingPlugin{version: "0123456789abcdef0123456789abcdef:1", content: "live"}
}

// SupportsFileVersionPreconditions marks this backend as CAS-capable, which the
// shared resolver requires before it will run any conditional mutation.
func (p *recordingPlugin) SupportsFileVersionPreconditions() bool { return true }

func (p *recordingPlugin) Name() string { return "recording" }

func (p *recordingPlugin) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{SupportsRename: true, SupportsVersions: true, SupportsRandomIO: true}
}

// observe records the per-call plugin routing selection the interface threaded in.
func (p *recordingPlugin) observe(ctx context.Context) {
	p.plugins = append(p.plugins, mcpplugin.OverrideFromContext(ctx))
}

// check applies the caller's condition exactly as the storage layer would, so a
// stale or absent token cannot be silently accepted by this fixture either.
func (p *recordingPlugin) check(ctx context.Context, auth files.AuthContext, project, path string,
	operation files.FileOperation,
) error {
	conditions, ok := files.ScopedFilePreconditions(ctx, auth, project, path, operation)
	if !ok {
		return files.NewError(files.ErrCodePreconditionRequired, "no precondition reached the backend", false)
	}
	if conditions.CreateOnly {
		return files.NewError(files.ErrCodeAlreadyExists, "file already exists", false)
	}
	if conditions.ExpectedVersion != p.version {
		return files.NewError(files.ErrCodeVersionConflict, "version conflict", false)
	}
	return nil
}

func (p *recordingPlugin) Stat(_ context.Context, _ files.AuthContext, _, _ string) (files.StatResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return files.StatResult{Exists: true, Type: files.FileTypeFile, Size: int64(len(p.content)),
		CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000001, 0).UTC(),
		Version: p.version}, nil
}

func (p *recordingPlugin) Read(ctx context.Context, _ files.AuthContext, _, _ string, _, _ int64) (files.ReadResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	p.observe(ctx)
	return files.ReadResult{Content: p.content, ContentEncoding: "utf-8", Version: p.version}, nil
}

func (p *recordingPlugin) Write(ctx context.Context, auth files.AuthContext, project, path, content, _ string,
	_ int64, _ files.WriteMode,
) (files.WriteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	p.attempts++
	if err := p.check(ctx, auth, project, path, files.FileOperationWrite); err != nil {
		return files.WriteResult{}, err
	}
	p.writes++
	p.content = content
	p.version = "0123456789abcdef0123456789abcdef:2"
	return files.WriteResult{BytesWritten: int64(len(content)), Version: p.version}, nil
}

func (p *recordingPlugin) Delete(ctx context.Context, auth files.AuthContext, project, path string, _ bool) (files.DeleteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	p.attempts++
	if err := p.check(ctx, auth, project, path, files.FileOperationDelete); err != nil {
		return files.DeleteResult{}, err
	}
	p.deletes++
	return files.DeleteResult{DeletedCount: 1}, nil
}

func (p *recordingPlugin) Rename(ctx context.Context, auth files.AuthContext, project, fromPath, _ string, _ bool) (files.RenameResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	p.attempts++
	if err := p.check(ctx, auth, project, fromPath, files.FileOperationRename); err != nil {
		return files.RenameResult{}, err
	}
	p.renames++
	return files.RenameResult{MovedCount: 1}, nil
}

func (p *recordingPlugin) List(ctx context.Context, _ files.AuthContext, _, _ string, _, _ int) (files.ListResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	return files.ListResult{Entries: []files.FileEntry{{Name: "a", Path: "/a", Type: files.FileTypeFile,
		Size: 4000000000, CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000001, 0).UTC()}}}, nil
}

func (p *recordingPlugin) Search(ctx context.Context, _ files.AuthContext, _, _, _ string, _ int) (files.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	return files.SearchResult{Chunks: []files.ChunkEntry{}}, nil
}

func (p *recordingPlugin) Start(context.Context) error { return nil }
func (p *recordingPlugin) Stop(context.Context) error  { return nil }

// newContractResolver builds the real query/mutation resolvers without the
// Telegram controller, which needs a full runtime configuration this test does
// not load. Every field under test is wired exactly as in production.
func newContractResolver(args ResolverArgs) *Resolver {
	resolver := &Resolver{args: args}
	resolver.queryResolver = resolver.buildQueryResolver()
	resolver.mutationResolver = resolver.buildMutationResolver()
	return resolver
}

// graphQLResponse is the decoded envelope, including the shared error contract.
type graphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Path       []any          `json:"path"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

// execute runs one operation against the real generated schema and resolver.
// Passing authorize=false omits the Authorization header entirely.
func execute(t *testing.T, resolver *Resolver, authorize bool, query string) graphQLResponse {
	t.Helper()
	server := handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: resolver}))
	body, err := json.Marshal(map[string]string{"query": query})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	if authorize {
		request = request.WithContext(mcpauth.WithContext(request.Context(), &mcpauth.Context{
			APIKey: "sk-contract-test", APIKeyHash: "hash-contract-test",
			UserID: "user-1", UserIdentity: "user-1",
		}))
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var response graphQLResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

// errorCode extracts the shared machine-stable code from the first error.
func errorCode(t *testing.T, response graphQLResponse) string {
	t.Helper()
	require.NotEmpty(t, response.Errors, "expected an error response")
	code, ok := response.Errors[0].Extensions["code"].(string)
	require.True(t, ok, "error must carry the shared code extension: %+v", response.Errors[0].Extensions)
	return code
}

// TestGraphQLFileIOMutationContract is the behavioral half of the FileIO
// parity contract. It executes real GraphQL operations through the generated
// schema and the shared precondition gate, and asserts that no mutation reaches
// storage without the concurrency intent MCP also demands.
func TestGraphQLFileIOMutationContract(t *testing.T) {
	t.Run("write without a precondition is rejected before any storage call", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileWrite(project:"p",path:"/a",content:"new") { bytes_written version } }`)
		require.Equal(t, string(files.ErrCodePreconditionRequired), errorCode(t, response))
		require.Zero(t, plugin.attempts, "the shared gate must reject before the backend is called at all")
		require.Zero(t, plugin.writes, "a rejected precondition must not write")
		require.Equal(t, "live", plugin.content)
	})

	t.Run("write with a stale version conflicts and leaves the file unchanged", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileWrite(project:"p",path:"/a",content:"new",mode:TRUNCATE,`+
				`expected_version:"0123456789abcdef0123456789abcdef:1") { bytes_written version } }`)
		require.Empty(t, response.Errors, "the current version must be accepted")
		require.Equal(t, 1, plugin.writes)

		stale := execute(t, resolver, true,
			`mutation { FileWrite(project:"p",path:"/a",content:"newer",mode:TRUNCATE,`+
				`expected_version:"0123456789abcdef0123456789abcdef:1") { bytes_written version } }`)
		require.Equal(t, string(files.ErrCodeVersionConflict), errorCode(t, stale))
		require.Equal(t, 1, plugin.writes, "a conflict must not retry or overwrite")
		require.Equal(t, "new", plugin.content)
	})

	t.Run("expected_version and create_only are mutually exclusive", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileWrite(project:"p",path:"/a",content:"new",create_only:true,`+
				`expected_version:"0123456789abcdef0123456789abcdef:1") { version } }`)
		require.Equal(t, string(files.ErrCodeInvalidArgument), errorCode(t, response))
		require.Zero(t, plugin.attempts, "mutually exclusive conditions must not reach storage")
		require.Zero(t, plugin.writes)
	})

	t.Run("a malformed version is rejected without reaching storage", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileWrite(project:"p",path:"/a",content:"new",expected_version:"7") { version } }`)
		require.NotEmpty(t, response.Errors)
		require.Zero(t, plugin.attempts, "a malformed token must not reach storage")
		require.Zero(t, plugin.writes)
	})

	t.Run("the project root cannot be deleted through GraphQL either", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileDelete(project:"p",path:"",`+
				`expected_version:"0123456789abcdef0123456789abcdef:1") { deleted_count } }`)
		require.Equal(t, string(files.ErrCodePermissionDenied), errorCode(t, response))
		require.Zero(t, plugin.attempts, "root deletion must be refused before storage")
		require.Zero(t, plugin.deletes)
	})

	t.Run("rename destination conditions are mutually exclusive", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",`+
				`expected_version:"0123456789abcdef0123456789abcdef:1",`+
				`expected_destination_version:"0123456789abcdef0123456789abcdef:1",`+
				`destination_must_not_exist:true) { moved_count } }`)
		require.Equal(t, string(files.ErrCodeInvalidArgument), errorCode(t, response))
		require.Zero(t, plugin.attempts, "conflicting destination conditions must not reach storage")
		require.Zero(t, plugin.renames)
	})

	t.Run("an unauthorized request never reaches the backend", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, false,
			`mutation { FileWrite(project:"p",path:"/a",content:"new",`+
				`expected_version:"0123456789abcdef0123456789abcdef:1") { version } }`)
		require.Equal(t, string(files.ErrCodePermissionDenied), errorCode(t, response))
		require.Zero(t, plugin.attempts)
		require.Zero(t, plugin.writes)
		read := execute(t, resolver, false, `query { FileRead(project:"p",path:"/a") { content } }`)
		require.Equal(t, string(files.ErrCodePermissionDenied), errorCode(t, read))
		require.Zero(t, plugin.reads)
	})

	t.Run("a missing backend disables only its own field", func(t *testing.T) {
		resolver := newContractResolver(ResolverArgs{})
		response := execute(t, resolver, true, `query { FileRead(project:"p",path:"/a") { content } }`)
		require.Equal(t, string(files.ErrCodeSearchBackend), errorCode(t, response))
		// An unrelated field on the same schema stays reachable.
		hello := execute(t, resolver, true, `query { Hello }`)
		require.Empty(t, hello.Errors)
	})
}

// TestGraphQLFileIOSharedBehavior pins the read-path parity details that a
// second implementation would be likely to get wrong.
func TestGraphQLFileIOSharedBehavior(t *testing.T) {
	t.Run("byte counts above the 32-bit range survive exactly", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`query { FileList(project:"p",path:"") { entries { path size } has_more } }`)
		require.Empty(t, response.Errors)
		require.Contains(t, string(response.Data["FileList"]), `"size":"4000000000"`)
	})

	t.Run("an empty search result is an array, not null", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`query { FileSearch(project:"p",query:"needle") { chunks { file_path } } }`)
		require.Empty(t, response.Errors)
		require.JSONEq(t, `{"chunks":[]}`, string(response.Data["FileSearch"]))
	})

	t.Run("an empty query is rejected before the backend is consulted", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`query { FileSearch(project:"p",query:"   ") { chunks { file_path } } }`)
		require.Equal(t, string(files.ErrCodeInvalidArgument), errorCode(t, response))
	})

	t.Run("the per-call plugin selection reaches the manager", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		require.Empty(t, execute(t, resolver, true,
			`query { FileRead(project:"p",path:"/a",plugin:PAGEINDEX) { content } }`).Errors)
		require.Contains(t, plugin.plugins, mcpplugin.DefaultPluginPageIndex)

		require.Empty(t, execute(t, resolver, true,
			`query { FileRead(project:"p",path:"/a") { content } }`).Errors)
		require.Contains(t, plugin.plugins, mcpplugin.DefaultPluginAuto)
	})

	t.Run("an unconditional read still works, so a client can obtain a token", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true, `query { FileRead(project:"p",path:"/a") { content version } }`)
		require.Empty(t, response.Errors)
		require.Contains(t, string(response.Data["FileRead"]), `"version":"0123456789abcdef0123456789abcdef:1"`)
	})
}

// TestGraphQLAndMCPShareOneConditionalGate proves the two interfaces route
// through the same implementation rather than two similar-looking copies.
func TestGraphQLAndMCPShareOneConditionalGate(t *testing.T) {
	plugin := newRecordingPlugin()
	auth := files.AuthContext{APIKey: "sk", APIKeyHash: "hash", UserID: "u", UserIdentity: "u"}

	// MCP's own entry point with no conditions must be rejected identically.
	_, _, err := mcptools.ConditionalFileService(context.Background(), plugin, auth,
		"p", "/a", "", files.FileOperationWrite, files.FilePreconditions{})
	require.Error(t, err)
	require.True(t, files.IsCode(err, files.ErrCodePreconditionRequired))

	resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
	response := execute(t, resolver, true,
		`mutation { FileWrite(project:"p",path:"/a",content:"x") { version } }`)
	require.Equal(t, string(files.ErrCodePreconditionRequired), errorCode(t, response),
		"GraphQL must surface the same code the shared gate produced for MCP")
	require.Zero(t, plugin.attempts, "both interfaces must stop before the backend, not inside it")
	require.Zero(t, plugin.writes)
}
