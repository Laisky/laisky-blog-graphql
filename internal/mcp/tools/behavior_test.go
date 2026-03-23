package tools

import (
	"context"
	"encoding/json"
	"testing"

	goerrors "github.com/Laisky/errors/v2"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func behaviorReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func behaviorAuthCtx() context.Context {
	auth, _ := mcpauth.DeriveFromAPIKey("sk-behavior-test-key")
	ctx := mcpauth.WithContext(context.Background(), auth)
	return context.WithValue(ctx, ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "sk-behavior-test-key",
		APIKeyHash:   auth.APIKeyHash,
		UserIdentity: auth.UserIdentity,
	})
}

func behaviorTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	tc, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

func behaviorJSONContent(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	text := behaviorTextContent(t, result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	return payload
}

// ---------------------------------------------------------------------------
// mock file service for behavior tests
// ---------------------------------------------------------------------------

type behaviorFileService struct {
	statResult   files.StatResult
	statErr      error
	readResult   files.ReadResult
	readErr      error
	writeResult  files.WriteResult
	writeErr     error
	deleteResult files.DeleteResult
	deleteErr    error
	renameResult files.RenameResult
	renameErr    error
	listResult   files.ListResult
	listErr      error
	searchResult files.SearchResult
	searchErr    error

	lastProject string
	lastPath    string
	lastMode    files.WriteMode
}

func (m *behaviorFileService) Stat(_ context.Context, _ files.AuthContext, project, path string) (files.StatResult, error) {
	m.lastProject = project
	m.lastPath = path
	return m.statResult, m.statErr
}

func (m *behaviorFileService) Read(_ context.Context, _ files.AuthContext, project, path string, _ int64, _ int64) (files.ReadResult, error) {
	m.lastProject = project
	m.lastPath = path
	return m.readResult, m.readErr
}

func (m *behaviorFileService) Write(_ context.Context, _ files.AuthContext, project, path string, _ string, _ string, _ int64, mode files.WriteMode) (files.WriteResult, error) {
	m.lastProject = project
	m.lastPath = path
	m.lastMode = mode
	return m.writeResult, m.writeErr
}

func (m *behaviorFileService) Delete(_ context.Context, _ files.AuthContext, project, path string, _ bool) (files.DeleteResult, error) {
	m.lastProject = project
	m.lastPath = path
	return m.deleteResult, m.deleteErr
}

func (m *behaviorFileService) Rename(_ context.Context, _ files.AuthContext, project, from string, _ string, _ bool) (files.RenameResult, error) {
	m.lastProject = project
	m.lastPath = from
	return m.renameResult, m.renameErr
}

func (m *behaviorFileService) List(_ context.Context, _ files.AuthContext, project, path string, _ int, _ int) (files.ListResult, error) {
	m.lastProject = project
	m.lastPath = path
	return m.listResult, m.listErr
}

func (m *behaviorFileService) Search(_ context.Context, _ files.AuthContext, project, query string, _ string, _ int) (files.SearchResult, error) {
	m.lastProject = project
	m.lastPath = query
	return m.searchResult, m.searchErr
}

// ---------------------------------------------------------------------------
// Tool construction nil guard tests
// ---------------------------------------------------------------------------

func TestNewToolNilDependencies(t *testing.T) {
	t.Parallel()

	t.Run("NewWebSearchTool nil provider", func(t *testing.T) {
		_, err := NewWebSearchTool(nil, log.Logger, func(context.Context) string { return "" }, func(context.Context, string, oneapi.Price, string) error { return nil })
		require.Error(t, err)
	})

	t.Run("NewWebSearchTool nil logger", func(t *testing.T) {
		_, err := NewWebSearchTool(&stubSearchProvider{}, nil, func(context.Context) string { return "" }, func(context.Context, string, oneapi.Price, string) error { return nil })
		require.Error(t, err)
	})

	t.Run("NewWebSearchTool nil key provider", func(t *testing.T) {
		_, err := NewWebSearchTool(&stubSearchProvider{}, log.Logger, nil, func(context.Context, string, oneapi.Price, string) error { return nil })
		require.Error(t, err)
	})

	t.Run("NewWebSearchTool nil billing checker", func(t *testing.T) {
		_, err := NewWebSearchTool(&stubSearchProvider{}, log.Logger, func(context.Context) string { return "" }, nil)
		require.Error(t, err)
	})

	t.Run("NewFileStatTool nil service", func(t *testing.T) {
		_, err := NewFileStatTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileReadTool nil service", func(t *testing.T) {
		_, err := NewFileReadTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileWriteTool nil service", func(t *testing.T) {
		_, err := NewFileWriteTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileDeleteTool nil service", func(t *testing.T) {
		_, err := NewFileDeleteTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileRenameTool nil service", func(t *testing.T) {
		_, err := NewFileRenameTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileListTool nil service", func(t *testing.T) {
		_, err := NewFileListTool(nil)
		require.Error(t, err)
	})

	t.Run("NewFileSearchTool nil service", func(t *testing.T) {
		_, err := NewFileSearchTool(nil)
		require.Error(t, err)
	})

	t.Run("NewMCPPipeTool nil logger", func(t *testing.T) {
		_, err := NewMCPPipeTool(nil, func(context.Context, string, any) (*mcp.CallToolResult, error) { return nil, nil }, PipeLimits{})
		require.Error(t, err)
	})

	t.Run("NewMCPPipeTool nil invoker", func(t *testing.T) {
		_, err := NewMCPPipeTool(log.Logger, nil, PipeLimits{})
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// File tools: missing authorization returns permission denied
// ---------------------------------------------------------------------------

func TestFileToolsMissingAuth(t *testing.T) {
	t.Parallel()
	svc := &behaviorFileService{}

	tools := []struct {
		name    string
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}{
		{"file_stat", mustFileStat(t, svc)},
		{"file_read", mustFileRead(t, svc)},
		{"file_write", mustFileWrite(t, svc)},
		{"file_delete", mustFileDelete(t, svc)},
		{"file_rename", mustFileRename(t, svc)},
		{"file_list", mustFileList(t, svc)},
		{"file_search", mustFileSearch(t, svc)},
	}

	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// No auth context => should get permission denied
			result, err := tc.handler(context.Background(), behaviorReq(map[string]any{
				"project":   "proj",
				"path":      "/test.txt",
				"content":   "x",
				"query":     "x",
				"from_path": "/a",
				"to_path":   "/b",
			}))
			require.NoError(t, err)
			require.True(t, result.IsError)
			payload := behaviorJSONContent(t, result)
			require.Equal(t, string(files.ErrCodePermissionDenied), payload["code"])
		})
	}
}

// ---------------------------------------------------------------------------
// File tools: missing required parameters
// ---------------------------------------------------------------------------

func TestFileToolsMissingRequiredParams(t *testing.T) {
	t.Parallel()
	svc := &behaviorFileService{}
	ctx := behaviorAuthCtx()

	t.Run("file_stat missing project", func(t *testing.T) {
		tool, _ := NewFileStatTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_read missing project", func(t *testing.T) {
		tool, _ := NewFileReadTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"path": "/f.txt"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_read missing path", func(t *testing.T) {
		tool, _ := NewFileReadTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "proj"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_write missing content", func(t *testing.T) {
		tool, _ := NewFileWriteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "proj", "path": "/f.txt"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_rename missing from_path", func(t *testing.T) {
		tool, _ := NewFileRenameTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "proj", "to_path": "/b"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_rename missing to_path", func(t *testing.T) {
		tool, _ := NewFileRenameTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "proj", "from_path": "/a"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_search missing query", func(t *testing.T) {
		tool, _ := NewFileSearchTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "proj"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})
}

// ---------------------------------------------------------------------------
// File tools: service errors are returned as tool errors
// ---------------------------------------------------------------------------

func TestFileToolsServiceErrors(t *testing.T) {
	t.Parallel()
	ctx := behaviorAuthCtx()

	t.Run("file_stat not found", func(t *testing.T) {
		svc := &behaviorFileService{statErr: files.NewError(files.ErrCodeNotFound, "not found", false)}
		tool, _ := NewFileStatTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/x"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, string(files.ErrCodeNotFound), payload["code"])
		require.Equal(t, false, payload["retryable"])
	})

	t.Run("file_read backend error retryable", func(t *testing.T) {
		svc := &behaviorFileService{readErr: files.NewError(files.ErrCodeSearchBackend, "timeout", true)}
		tool, _ := NewFileReadTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/x"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, true, payload["retryable"])
	})

	t.Run("file_write quota exceeded", func(t *testing.T) {
		svc := &behaviorFileService{writeErr: files.NewError(files.ErrCodeQuotaExceeded, "disk full", false)}
		tool, _ := NewFileWriteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/x", "content": "data"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, string(files.ErrCodeQuotaExceeded), payload["code"])
	})

	t.Run("file_delete permission denied", func(t *testing.T) {
		svc := &behaviorFileService{deleteErr: files.NewError(files.ErrCodePermissionDenied, "denied", false)}
		tool, _ := NewFileDeleteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/x"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, string(files.ErrCodePermissionDenied), payload["code"])
	})

	t.Run("file_rename already exists", func(t *testing.T) {
		svc := &behaviorFileService{renameErr: files.NewError(files.ErrCodeAlreadyExists, "exists", false)}
		tool, _ := NewFileRenameTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "from_path": "/a", "to_path": "/b"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_list root not found returns empty", func(t *testing.T) {
		svc := &behaviorFileService{listErr: files.NewError(files.ErrCodeNotFound, "not found", false)}
		tool, _ := NewFileListTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": ""}))
		require.NoError(t, err)
		require.False(t, result.IsError, "root listing not found should return empty, not error")
		payload := behaviorJSONContent(t, result)
		entries, _ := payload["entries"].([]any)
		require.Empty(t, entries)
	})

	t.Run("file_list non-root not found returns error", func(t *testing.T) {
		svc := &behaviorFileService{listErr: files.NewError(files.ErrCodeNotFound, "not found", false)}
		tool, _ := NewFileListTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/subdir"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("file_search generic error", func(t *testing.T) {
		svc := &behaviorFileService{searchErr: goerrors.New("unexpected error")}
		tool, _ := NewFileSearchTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "query": "hello"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})
}

// ---------------------------------------------------------------------------
// File tools: successful operations
// ---------------------------------------------------------------------------

func TestFileToolsSuccessfulOperations(t *testing.T) {
	t.Parallel()
	ctx := behaviorAuthCtx()

	t.Run("file_stat success", func(t *testing.T) {
		svc := &behaviorFileService{statResult: files.StatResult{Exists: true, Type: files.FileTypeFile, Size: 100}}
		tool, _ := NewFileStatTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/f.txt"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, true, payload["exists"])
		require.Equal(t, "FILE", payload["type"])
	})

	t.Run("file_read success", func(t *testing.T) {
		svc := &behaviorFileService{readResult: files.ReadResult{Content: "hello", ContentEncoding: "utf-8"}}
		tool, _ := NewFileReadTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/f.txt"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, "hello", payload["content"])
		require.Equal(t, "utf-8", payload["content_encoding"])
	})

	t.Run("file_write success", func(t *testing.T) {
		svc := &behaviorFileService{writeResult: files.WriteResult{BytesWritten: 5}}
		tool, _ := NewFileWriteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/f.txt", "content": "hello", "mode": "APPEND"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		require.Equal(t, files.WriteModeAppend, svc.lastMode)
	})

	t.Run("file_write mode case insensitive", func(t *testing.T) {
		svc := &behaviorFileService{writeResult: files.WriteResult{BytesWritten: 5}}
		tool, _ := NewFileWriteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/f.txt", "content": "hello", "mode": "truncate"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		require.Equal(t, files.WriteModeTruncate, svc.lastMode)
	})

	t.Run("file_delete success", func(t *testing.T) {
		svc := &behaviorFileService{deleteResult: files.DeleteResult{DeletedCount: 3}}
		tool, _ := NewFileDeleteTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/dir", "recursive": true}))
		require.NoError(t, err)
		require.False(t, result.IsError)
	})

	t.Run("file_rename success", func(t *testing.T) {
		svc := &behaviorFileService{renameResult: files.RenameResult{MovedCount: 1}}
		tool, _ := NewFileRenameTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "from_path": "/a", "to_path": "/b", "overwrite": true}))
		require.NoError(t, err)
		require.False(t, result.IsError)
	})

	t.Run("file_list success", func(t *testing.T) {
		svc := &behaviorFileService{listResult: files.ListResult{
			Entries: []files.FileEntry{{Name: "f.txt", Path: "/f.txt", Type: files.FileTypeFile}},
			HasMore: false,
		}}
		tool, _ := NewFileListTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "path": "/", "depth": 1}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		// "/" path should be normalized to ""
		require.Equal(t, "", svc.lastPath)
	})

	t.Run("file_search success", func(t *testing.T) {
		svc := &behaviorFileService{searchResult: files.SearchResult{Chunks: []files.ChunkEntry{{FilePath: "/f.txt", ChunkContent: "match"}}}}
		tool, _ := NewFileSearchTool(svc)
		result, err := tool.Handle(ctx, behaviorReq(map[string]any{"project": "p", "query": "find me"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
	})
}

// ---------------------------------------------------------------------------
// web_search: edge cases
// ---------------------------------------------------------------------------

func TestWebSearchMultipleResults(t *testing.T) {
	provider := &stubSearchProvider{
		items: []searchlib.SearchResultItem{
			{URL: "https://a.com", Name: "A", Snippet: "First"},
			{URL: "https://b.com", Name: "B", Snippet: "Second"},
			{URL: "https://c.com", Name: "C", Snippet: "Third"},
		},
	}

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "sk-key" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		provider,
	)

	result, err := tool.Handle(context.Background(), behaviorReq(map[string]any{"query": "test"}))
	require.NoError(t, err)
	require.False(t, result.IsError)

	var payload searchlib.SimplifiedSearchResult
	require.NoError(t, json.Unmarshal([]byte(behaviorTextContent(t, result)), &payload))
	require.Len(t, payload.Results, 3)
}

func TestWebSearchEmptyResults(t *testing.T) {
	provider := &stubSearchProvider{
		items: []searchlib.SearchResultItem{},
	}

	tool := mustWebSearchTool(t,
		func(context.Context) string { return "sk-key" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		provider,
	)

	result, err := tool.Handle(context.Background(), behaviorReq(map[string]any{"query": "nothing matches"}))
	require.NoError(t, err)
	require.False(t, result.IsError)

	var payload searchlib.SimplifiedSearchResult
	require.NoError(t, json.Unmarshal([]byte(behaviorTextContent(t, result)), &payload))
	require.Empty(t, payload.Results)
}

// ---------------------------------------------------------------------------
// web_fetch: output_markdown variants
// ---------------------------------------------------------------------------

func TestWebFetchOutputMarkdownVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    any
		expected bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"string yes", "yes", true},
		{"string no", "no", false},
		{"string 0", "0", false},
		{"string 1", "1", true},
		{"float64 0", float64(0), false},
		{"float64 1", float64(1), true},
		{"nil defaults true", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"url": "https://example.com"}
			if tc.value != nil {
				args["output_markdown"] = tc.value
			}
			require.Equal(t, tc.expected, resolveOutputMarkdownArg(args))
		})
	}
}

// ---------------------------------------------------------------------------
// sanitizeURLForLog
// ---------------------------------------------------------------------------

func TestSanitizeURLForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		contains string
		excludes string
	}{
		{"strips query", "https://example.com/path?key=secret", "https://example.com/path", "secret"},
		{"strips fragment", "https://example.com/path#section", "https://example.com/path", "#section"},
		{"trims whitespace", "  https://example.com  ", "https://example.com", ""},
		{"invalid URL passthrough", "not-a-url", "not-a-url", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := sanitizeURLForLog(tc.input)
			require.Contains(t, result, tc.contains)
			if tc.excludes != "" {
				require.NotContains(t, result, tc.excludes)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// mcp_pipe: PipeLimits defaults
// ---------------------------------------------------------------------------

func TestPipeLimitsDefaults(t *testing.T) {
	tool, err := NewMCPPipeTool(log.Logger, func(context.Context, string, any) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}, PipeLimits{})
	require.NoError(t, err)

	require.Equal(t, 50, tool.limits.MaxSteps)
	require.Equal(t, 5, tool.limits.MaxDepth)
	require.Equal(t, 8, tool.limits.MaxParallel)
}

func TestPipeLimitsCustom(t *testing.T) {
	tool, err := NewMCPPipeTool(log.Logger, func(context.Context, string, any) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}, PipeLimits{MaxSteps: 10, MaxDepth: 3, MaxParallel: 2})
	require.NoError(t, err)

	require.Equal(t, 10, tool.limits.MaxSteps)
	require.Equal(t, 3, tool.limits.MaxDepth)
	require.Equal(t, 2, tool.limits.MaxParallel)
}

// ---------------------------------------------------------------------------
// Tool definitions have correct names
// ---------------------------------------------------------------------------

func TestToolDefinitionNames(t *testing.T) {
	t.Parallel()
	svc := &behaviorFileService{}

	checks := []struct {
		name string
		tool Tool
	}{
		{"file_stat", mustFileStatTool(t, svc)},
		{"file_read", mustFileReadTool(t, svc)},
		{"file_write", mustFileWriteTool(t, svc)},
		{"file_delete", mustFileDeleteTool(t, svc)},
		{"file_rename", mustFileRenameTool(t, svc)},
		{"file_list", mustFileListTool(t, svc)},
		{"file_search", mustFileSearchTool(t, svc)},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			def := tc.tool.Definition()
			require.Equal(t, tc.name, def.Name)
			require.NotEmpty(t, def.Description)
		})
	}
}

// ---------------------------------------------------------------------------
// readArgHelper edge cases
// ---------------------------------------------------------------------------

func TestReadArgHelpers(t *testing.T) {
	t.Parallel()

	t.Run("readStringArg nil arguments", func(t *testing.T) {
		req := mcp.CallToolRequest{}
		require.Equal(t, "", readStringArg(req, "key"))
	})

	t.Run("readStringArg missing key", func(t *testing.T) {
		req := behaviorReq(map[string]any{"other": "val"})
		require.Equal(t, "", readStringArg(req, "key"))
	})

	t.Run("readIntArg float64 value", func(t *testing.T) {
		req := behaviorReq(map[string]any{"n": float64(42)})
		require.Equal(t, 42, readIntArg(req, "n"))
	})

	t.Run("readIntArg nil arguments", func(t *testing.T) {
		req := mcp.CallToolRequest{}
		require.Equal(t, 0, readIntArg(req, "n"))
	})

	t.Run("readInt64ArgWithDefault missing key", func(t *testing.T) {
		req := behaviorReq(map[string]any{})
		require.Equal(t, int64(-1), readInt64ArgWithDefault(req, "len", -1))
	})

	t.Run("readBoolArg true", func(t *testing.T) {
		req := behaviorReq(map[string]any{"flag": true})
		require.True(t, readBoolArg(req, "flag"))
	})

	t.Run("readBoolArg missing defaults false", func(t *testing.T) {
		req := behaviorReq(map[string]any{})
		require.False(t, readBoolArg(req, "flag"))
	})

	t.Run("readIntArgWithDefault missing uses default", func(t *testing.T) {
		req := behaviorReq(map[string]any{})
		require.Equal(t, 5, readIntArgWithDefault(req, "depth", 5))
	})

	t.Run("readIntArgWithDefault present overrides default", func(t *testing.T) {
		req := behaviorReq(map[string]any{"depth": float64(3)})
		require.Equal(t, 3, readIntArgWithDefault(req, "depth", 5))
	})
}

// ---------------------------------------------------------------------------
// fileToolErrorFromErr
// ---------------------------------------------------------------------------

func TestFileToolErrorFromErr(t *testing.T) {
	t.Parallel()

	t.Run("nil error returns nil", func(t *testing.T) {
		require.Nil(t, fileToolErrorFromErr(nil))
	})

	t.Run("typed error", func(t *testing.T) {
		err := files.NewError(files.ErrCodeNotFound, "gone", false)
		result := fileToolErrorFromErr(err)
		require.NotNil(t, result)
		require.True(t, result.IsError)
	})

	t.Run("untyped error", func(t *testing.T) {
		err := goerrors.New("random")
		result := fileToolErrorFromErr(err)
		require.NotNil(t, result)
		require.True(t, result.IsError)
	})
}

// ---------------------------------------------------------------------------
// normalizeFileListPath
// ---------------------------------------------------------------------------

func TestNormalizeFileListPath(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", normalizeFileListPath("/"))
	require.Equal(t, "", normalizeFileListPath(""))
	require.Equal(t, "/docs", normalizeFileListPath("/docs"))
	require.Equal(t, "subdir", normalizeFileListPath("subdir"))
}

// ---------------------------------------------------------------------------
// detectSearchMode
// ---------------------------------------------------------------------------

func TestDetectSearchModeBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query    string
		expected string
	}{
		{"file_.*", FindToolModeRegex},
		{"(?i)web", FindToolModeRegex},
		{"file_read", FindToolModeRegex},
		{"search files", FindToolModeBM25},
		{"how to read a file", FindToolModeBM25},
		{"web_search", FindToolModeRegex},
		{"", FindToolModeBM25},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.expected, detectSearchMode(tc.query))
		})
	}
}

// ---------------------------------------------------------------------------
// isToolNamePattern
// ---------------------------------------------------------------------------

func TestIsToolNamePatternBehavior(t *testing.T) {
	t.Parallel()

	require.True(t, isToolNamePattern("file_read"))
	require.True(t, isToolNamePattern("web_search"))
	require.True(t, isToolNamePattern("mcp_pipe"))
	require.False(t, isToolNamePattern(""))
	require.False(t, isToolNamePattern("search files"))
	require.False(t, isToolNamePattern("noseparator"))
}

// ---------------------------------------------------------------------------
// tool construction helpers
// ---------------------------------------------------------------------------

func mustFileStat(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileStatTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileRead(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileReadTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileWrite(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileWriteTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileDelete(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileDeleteTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileRename(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileRenameTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileList(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileSearch(t *testing.T, svc FileService) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tool, err := NewFileSearchTool(svc)
	require.NoError(t, err)
	return tool.Handle
}

func mustFileStatTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileStatTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileReadTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileReadTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileWriteTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileWriteTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileDeleteTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileDeleteTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileRenameTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileRenameTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileListTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileListTool(svc)
	require.NoError(t, err)
	return tool
}

func mustFileSearchTool(t *testing.T, svc FileService) Tool {
	t.Helper()
	tool, err := NewFileSearchTool(svc)
	require.NoError(t, err)
	return tool
}
