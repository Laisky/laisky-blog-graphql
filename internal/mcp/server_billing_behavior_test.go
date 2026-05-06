package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	srv "github.com/mark3labs/mcp-go/server"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

type behaviorFileService struct {
	mu    sync.Mutex
	calls []string
	err   map[string]error
}

func (s *behaviorFileService) record(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	if s.err == nil {
		return nil
	}
	return s.err[name]
}

func (s *behaviorFileService) callCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	for _, call := range s.calls {
		if call == name {
			count++
		}
	}

	return count
}

func (s *behaviorFileService) Stat(_ context.Context, _ files.AuthContext, _ string, _ string) (files.StatResult, error) {
	if err := s.record("stat"); err != nil {
		return files.StatResult{}, err
	}
	return files.StatResult{Exists: true, Type: files.FileTypeFile, Size: 7}, nil
}

func (s *behaviorFileService) Read(_ context.Context, _ files.AuthContext, _ string, _ string, _ int64, _ int64) (files.ReadResult, error) {
	if err := s.record("read"); err != nil {
		return files.ReadResult{}, err
	}
	return files.ReadResult{Content: "hello", ContentEncoding: "utf-8"}, nil
}

func (s *behaviorFileService) Write(_ context.Context, _ files.AuthContext, _ string, _ string, content string, _ string, _ int64, _ files.WriteMode) (files.WriteResult, error) {
	if err := s.record("write"); err != nil {
		return files.WriteResult{}, err
	}
	return files.WriteResult{BytesWritten: int64(len(content))}, nil
}

func (s *behaviorFileService) Delete(_ context.Context, _ files.AuthContext, _ string, _ string, _ bool) (files.DeleteResult, error) {
	if err := s.record("delete"); err != nil {
		return files.DeleteResult{}, err
	}
	return files.DeleteResult{DeletedCount: 1}, nil
}

func (s *behaviorFileService) Rename(_ context.Context, _ files.AuthContext, _ string, _ string, _ string, _ bool) (files.RenameResult, error) {
	if err := s.record("rename"); err != nil {
		return files.RenameResult{}, err
	}
	return files.RenameResult{MovedCount: 1}, nil
}

func (s *behaviorFileService) List(_ context.Context, _ files.AuthContext, _ string, _ string, _ int, _ int) (files.ListResult, error) {
	if err := s.record("list"); err != nil {
		return files.ListResult{}, err
	}
	return files.ListResult{Entries: []files.FileEntry{{Name: "a.txt", Path: "/a.txt", Type: files.FileTypeFile, Size: 5}}}, nil
}

func (s *behaviorFileService) Search(_ context.Context, _ files.AuthContext, _ string, _ string, _ string, _ int) (files.SearchResult, error) {
	if err := s.record("search"); err != nil {
		return files.SearchResult{}, err
	}
	return files.SearchResult{Chunks: []files.ChunkEntry{{FilePath: "/a.txt", ChunkContent: "hello", Score: 0.9}}}, nil
}

// Name reports the rag plugin identity for plugin.Plugin compatibility in tests.
func (s *behaviorFileService) Name() string { return mcpplugin.DefaultPluginRAG }

// Capabilities returns stub capabilities for plugin.Plugin compatibility in tests.
func (s *behaviorFileService) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{}
}

// Start is a no-op for plugin.Plugin compatibility in tests.
func (s *behaviorFileService) Start(context.Context) error { return nil }

// Stop is a no-op for plugin.Plugin compatibility in tests.
func (s *behaviorFileService) Stop(context.Context) error { return nil }

func (m *behaviorBillingReporter) reasonCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts := make(map[string]int, len(m.calls))
	for _, call := range m.calls {
		counts[call.reason]++
	}

	return counts
}

func mustBehaviorFileToolsServer(t *testing.T, svc *behaviorFileService, billingReporter *behaviorBillingReporter) *Server {
	t.Helper()

	fileStatTool, err := tools.NewFileStatTool(svc)
	require.NoError(t, err)
	fileReadTool, err := tools.NewFileReadTool(svc)
	require.NoError(t, err)
	fileWriteTool, err := tools.NewFileWriteTool(svc)
	require.NoError(t, err)
	fileDeleteTool, err := tools.NewFileDeleteTool(svc)
	require.NoError(t, err)
	fileRenameTool, err := tools.NewFileRenameTool(svc)
	require.NoError(t, err)
	fileListTool, err := tools.NewFileListTool(svc)
	require.NoError(t, err)
	fileSearchTool, err := tools.NewFileSearchTool(svc)
	require.NoError(t, err)

	return &Server{
		logger:          log.Logger,
		billingReporter: billingReporter.Check,
		fileStat:        fileStatTool,
		fileRead:        fileReadTool,
		fileWrite:       fileWriteTool,
		fileDelete:      fileDeleteTool,
		fileRename:      fileRenameTool,
		fileList:        fileListTool,
		fileSearch:      fileSearchTool,
		toolHandlers:    make(map[string]srv.ToolHandlerFunc),
	}
}

func registerBehaviorToolHandlers(s *Server) {
	s.toolHandlers["web_search"] = s.handleWebSearch
	s.toolHandlers["file_stat"] = s.handleFileStat
	s.toolHandlers["file_read"] = s.handleFileRead
	s.toolHandlers["file_write"] = s.handleFileWrite
	s.toolHandlers["file_delete"] = s.handleFileDelete
	s.toolHandlers["file_rename"] = s.handleFileRename
	s.toolHandlers["file_list"] = s.handleFileList
	s.toolHandlers["file_search"] = s.handleFileSearch
	if s.mcpPipe != nil {
		s.toolHandlers["mcp_pipe"] = s.handleMCPPipe
	}
}

func behaviorPipeInvoker(s *Server) tools.PipeInvoker {
	return func(ctx context.Context, toolName string, args any) (*mcpgo.CallToolResult, error) {
		if toolName == "mcp_pipe" {
			return mcpgo.NewToolResultError("mcp_pipe cannot invoke itself"), nil
		}

		handler, ok := s.toolHandlers[toolName]
		if !ok {
			return mcpgo.NewToolResultError("unknown tool"), nil
		}

		req := mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: toolName, Arguments: args}}
		return handler(ctx, req)
	}
}

func TestAllConfiguredFileIOHandlersReportZeroCostBilling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     string
		serviceKey string
		handler    func(*Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
		args       map[string]any
	}{
		{
			name:       "file_stat",
			reason:     "file_stat",
			serviceKey: "stat",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileStat
			},
			args: map[string]any{"project": "graphql", "path": "/a.txt"},
		},
		{
			name:       "file_read",
			reason:     "file_read",
			serviceKey: "read",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileRead
			},
			args: map[string]any{"project": "graphql", "path": "/a.txt"},
		},
		{
			name:       "file_write",
			reason:     "file_write",
			serviceKey: "write",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileWrite
			},
			args: map[string]any{"project": "graphql", "path": "/a.txt", "content": "hello", "mode": "APPEND"},
		},
		{
			name:       "file_delete",
			reason:     "file_delete",
			serviceKey: "delete",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileDelete
			},
			args: map[string]any{"project": "graphql", "path": "/a.txt", "recursive": false},
		},
		{
			name:       "file_rename",
			reason:     "file_rename",
			serviceKey: "rename",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileRename
			},
			args: map[string]any{"project": "graphql", "from_path": "/a.txt", "to_path": "/b.txt", "overwrite": true},
		},
		{
			name:       "file_list",
			reason:     "file_list",
			serviceKey: "list",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileList
			},
			args: map[string]any{"project": "graphql", "path": "/", "depth": 1, "limit": 10},
		},
		{
			name:       "file_search",
			reason:     "file_search",
			serviceKey: "search",
			handler: func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return s.handleFileSearch
			},
			args: map[string]any{"project": "graphql", "query": "hello", "path_prefix": "/", "limit": 5},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &behaviorFileService{}
			billingReporter := &behaviorBillingReporter{}
			s := mustBehaviorFileToolsServer(t, svc, billingReporter)

			result, err := tc.handler(s)(ctxWithAuthKey("sk-fileio"), makeReq(tc.args))
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Equal(t, 1, svc.callCount(tc.serviceKey))
			require.Eventually(t, func() bool { return billingReporter.count() == 1 }, time.Second, 10*time.Millisecond)
			require.Equal(t, "sk-fileio", billingReporter.last().apiKey)
			require.Equal(t, oneapi.Price(0), billingReporter.last().price)
			require.Equal(t, tc.reason, billingReporter.last().reason)
		})
	}
}

func TestMCPPipeComplexNestedBillingForFreeAndPaidSteps(t *testing.T) {
	t.Parallel()

	var preflightCalls int
	svc := &behaviorFileService{}
	billingReporter := &behaviorBillingReporter{}
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{Items: []searchlib.SearchResultItem{{URL: "https://example.com"}}}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-pipe" },
		func(ctx context.Context, _ string, _ oneapi.Price, _ string) error {
			preflightCalls++
			markBillingAttempted(ctx)
			return nil
		},
	)
	require.NoError(t, err)

	s := mustBehaviorFileToolsServer(t, svc, billingReporter)
	s.webSearch = webSearchTool
	s.mcpPipe = mustBehaviorPipeTool(t, behaviorPipeInvoker(s))
	registerBehaviorToolHandlers(s)

	result, handleErr := s.handleMCPPipe(ctxWithAuthKey("sk-pipe"), makeReq(map[string]any{
		"continue_on_error": false,
		"steps": []any{
			map[string]any{"id": "seed_search", "tool": "web_search", "args": map[string]any{"query": "seed"}},
			map[string]any{"id": "inspect", "tool": "file_stat", "args": map[string]any{"project": "graphql", "path": "/a.txt"}},
			map[string]any{"id": "branch", "pipe": map[string]any{
				"continue_on_error": false,
				"steps": []any{
					map[string]any{"id": "fanout", "parallel": []any{
						map[string]any{"id": "list", "tool": "file_list", "args": map[string]any{"project": "graphql", "path": "/", "depth": 1}},
						map[string]any{"id": "read", "tool": "file_read", "args": map[string]any{"project": "graphql", "path": "/a.txt"}},
						map[string]any{"id": "search", "tool": "file_search", "args": map[string]any{"project": "graphql", "query": "hello"}},
						map[string]any{"id": "inner", "pipe": map[string]any{
							"continue_on_error": false,
							"steps": []any{
								map[string]any{"id": "write", "tool": "file_write", "args": map[string]any{"project": "graphql", "path": "/a.txt", "content": "hello"}},
								map[string]any{"id": "refine", "tool": "web_search", "args": map[string]any{"query": "refine"}},
							},
						}},
					}},
					map[string]any{"id": "rename", "tool": "file_rename", "args": map[string]any{"project": "graphql", "from_path": "/a.txt", "to_path": "/b.txt", "overwrite": true}},
				},
			}},
			map[string]any{"id": "delete", "tool": "file_delete", "args": map[string]any{"project": "graphql", "path": "/b.txt"}},
		},
	}))
	require.NoError(t, handleErr)
	require.False(t, result.IsError)
	require.Equal(t, 2, preflightCalls)
	require.Equal(t, 1, svc.callCount("stat"))
	require.Equal(t, 1, svc.callCount("list"))
	require.Equal(t, 1, svc.callCount("read"))
	require.Equal(t, 1, svc.callCount("search"))
	require.Equal(t, 1, svc.callCount("write"))
	require.Equal(t, 1, svc.callCount("rename"))
	require.Equal(t, 1, svc.callCount("delete"))
	require.Eventually(t, func() bool {
		counts := billingReporter.reasonCounts()
		return counts["mcp_pipe"] == 1 &&
			counts["file_stat"] == 1 &&
			counts["file_list"] == 1 &&
			counts["file_read"] == 1 &&
			counts["file_search"] == 1 &&
			counts["file_write"] == 1 &&
			counts["file_rename"] == 1 &&
			counts["file_delete"] == 1 &&
			len(counts) == 8
	}, time.Second, 10*time.Millisecond)
}

func TestMCPPipeContinueOnErrorReportsEveryFreeFileIOAttempt(t *testing.T) {
	t.Parallel()

	var preflightCalls int
	svc := &behaviorFileService{err: map[string]error{
		"read":  files.NewError(files.ErrCodeNotFound, "not found", false),
		"write": files.NewError(files.ErrCodeQuotaExceeded, "disk full", false),
	}}
	billingReporter := &behaviorBillingReporter{}
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{Items: []searchlib.SearchResultItem{{URL: "https://example.com"}}}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-pipe" },
		func(ctx context.Context, _ string, _ oneapi.Price, _ string) error {
			preflightCalls++
			markBillingAttempted(ctx)
			return nil
		},
	)
	require.NoError(t, err)

	s := mustBehaviorFileToolsServer(t, svc, billingReporter)
	s.webSearch = webSearchTool
	s.mcpPipe = mustBehaviorPipeTool(t, behaviorPipeInvoker(s))
	registerBehaviorToolHandlers(s)

	result, handleErr := s.handleMCPPipe(ctxWithAuthKey("sk-pipe"), makeReq(map[string]any{
		"continue_on_error": true,
		"steps": []any{
			map[string]any{"id": "fanout", "parallel": []any{
				map[string]any{"id": "read1", "tool": "file_read", "args": map[string]any{"project": "graphql", "path": "/a.txt"}},
				map[string]any{"id": "read2", "tool": "file_read", "args": map[string]any{"project": "graphql", "path": "/b.txt"}},
				map[string]any{"id": "inner", "pipe": map[string]any{
					"continue_on_error": true,
					"steps": []any{
						map[string]any{"id": "paid", "tool": "web_search", "args": map[string]any{"query": "keep-going"}},
						map[string]any{"id": "write", "tool": "file_write", "args": map[string]any{"project": "graphql", "path": "/c.txt", "content": "hello"}},
						map[string]any{"id": "read3", "tool": "file_read", "args": map[string]any{"project": "graphql", "path": "/c.txt"}},
					},
				}},
			}},
			map[string]any{"id": "list", "tool": "file_list", "args": map[string]any{"project": "graphql", "path": "/", "depth": 1}},
		},
	}))
	require.NoError(t, handleErr)
	require.True(t, result.IsError)
	require.Equal(t, 1, preflightCalls)
	require.Equal(t, 3, svc.callCount("read"))
	require.Equal(t, 1, svc.callCount("write"))
	require.Equal(t, 1, svc.callCount("list"))
	require.Eventually(t, func() bool {
		counts := billingReporter.reasonCounts()
		return counts["mcp_pipe"] == 1 &&
			counts["file_read"] == 3 &&
			counts["file_write"] == 1 &&
			counts["file_list"] == 1 &&
			len(counts) == 4
	}, time.Second, 10*time.Millisecond)
}
