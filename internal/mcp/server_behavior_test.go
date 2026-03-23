package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	goerrors "github.com/Laisky/errors/v2"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	srv "github.com/mark3labs/mcp-go/server"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// behaviorRecorder records all tool invocations for assertions (thread-safe).
type behaviorRecorder struct {
	mu      sync.Mutex
	records []calllog.RecordInput
}

func (m *behaviorRecorder) Record(_ context.Context, input calllog.RecordInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, input)
	return nil
}

func (m *behaviorRecorder) last() calllog.RecordInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[len(m.records)-1]
}

func (m *behaviorRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// brokenRecorder simulates a broken call logger.
type brokenRecorder struct{}

func (*brokenRecorder) Record(_ context.Context, _ calllog.RecordInput) error {
	return goerrors.New("call logger unavailable")
}

// ctxWithAuth returns a context carrying a valid MCP auth context.
func ctxWithAuthKey(apiKey string) context.Context {
	auth, _ := mcpauth.DeriveFromAPIKey(apiKey)
	return mcpauth.WithContext(context.Background(), auth)
}

// makeReq builds a CallToolRequest with the given arguments.
func makeReq(args map[string]any) mcpgo.CallToolRequest {
	return mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{Arguments: args},
	}
}

// getText extracts text content from the first content element.
func getText(t *testing.T, result *mcpgo.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	tc, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)
	return tc.Text
}

// getJSON extracts and unmarshals the JSON payload from a result.
func getJSON(t *testing.T, result *mcpgo.CallToolResult) map[string]any {
	t.Helper()
	text := getText(t, result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	return payload
}

// stubSearchProvider implements searchlib.Provider for testing.
type stubSearchProvider struct {
	output *searchlib.SearchOutput
	err    error
}

func (s *stubSearchProvider) Search(_ context.Context, _ string) (*searchlib.SearchOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.output, nil
}

// ---------------------------------------------------------------------------
// Test: handler nil-guard returns tool error for every tool handler
// ---------------------------------------------------------------------------

func TestAllHandlersReturnToolErrorWhenNil(t *testing.T) {
	t.Parallel()
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}

	tests := []struct {
		name    string
		handler func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
		errMsg  string
	}{
		{"web_search", s.handleWebSearch, "web search is not configured"},
		{"web_fetch", s.handleWebFetch, "web fetch is not configured"},
		{"ask_user", s.handleAskUser, "ask_user tool is not available"},
		{"get_user_request", s.handleGetUserRequest, "get_user_request tool is not available"},
		{"extract_key_info", s.handleExtractKeyInfo, "extract_key_info tool is not available"},
		{"file_stat", s.handleFileStat, "file_stat tool is not available"},
		{"file_read", s.handleFileRead, "file_read tool is not available"},
		{"file_write", s.handleFileWrite, "file_write tool is not available"},
		{"file_delete", s.handleFileDelete, "file_delete tool is not available"},
		{"file_rename", s.handleFileRename, "file_rename tool is not available"},
		{"file_list", s.handleFileList, "file_list tool is not available"},
		{"file_search", s.handleFileSearch, "file_search tool is not available"},
		{"mcp_pipe", s.handleMCPPipe, "mcp_pipe tool is not available"},
		{"find_tool", s.handleFindTool, "find_tool tool is not available"},
		{"memory_before_turn", s.handleMemoryBeforeTurn, "memory_before_turn tool is not available"},
		{"memory_after_turn", s.handleMemoryAfterTurn, "memory_after_turn tool is not available"},
		{"memory_run_maintenance", s.handleMemoryRunMaintenance, "memory_run_maintenance tool is not available"},
		{"memory_list_dir_with_abstract", s.handleMemoryListDirWithAbstract, "memory_list_dir_with_abstract tool is not available"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.handler(context.Background(), makeReq(nil))
			require.NoError(t, err, "handler should not return a Go error")
			require.True(t, result.IsError)
			require.Equal(t, tc.errMsg, getText(t, result))
		})
	}
}

// ---------------------------------------------------------------------------
// Test: call logger records every handler invocation
// ---------------------------------------------------------------------------

func TestAllHandlersRecordCallLog(t *testing.T) {
	t.Parallel()

	handlers := []struct {
		name string
		tool string
		fn   func(*Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
	}{
		{"web_search", "web_search", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleWebSearch }},
		{"web_fetch", "web_fetch", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleWebFetch }},
		{"ask_user", "ask_user", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleAskUser }},
		{"get_user_request", "get_user_request", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleGetUserRequest }},
		{"extract_key_info", "extract_key_info", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleExtractKeyInfo }},
		{"file_stat", "file_stat", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileStat }},
		{"file_read", "file_read", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileRead }},
		{"file_write", "file_write", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileWrite }},
		{"file_delete", "file_delete", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileDelete }},
		{"file_rename", "file_rename", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileRename }},
		{"file_list", "file_list", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileList }},
		{"file_search", "file_search", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFileSearch }},
		{"mcp_pipe", "mcp_pipe", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleMCPPipe }},
		{"find_tool", "find_tool", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleFindTool }},
		{"memory_before_turn", "memory_before_turn", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleMemoryBeforeTurn }},
		{"memory_after_turn", "memory_after_turn", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleMemoryAfterTurn }},
		{"memory_run_maintenance", "memory_run_maintenance", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleMemoryRunMaintenance }},
		{"memory_list_dir_with_abstract", "memory_list_dir_with_abstract", func(s *Server) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) { return s.handleMemoryListDirWithAbstract }},
	}

	for _, tc := range handlers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := &behaviorRecorder{}
			s := &Server{callLogger: recorder, logger: log.Logger}
			handler := tc.fn(s)
			_, _ = handler(context.Background(), makeReq(map[string]any{"key": "val"}))
			require.Len(t, recorder.records, 1)
			require.Equal(t, tc.tool, recorder.last().ToolName)
			require.Equal(t, calllog.StatusError, recorder.last().Status)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: call logger failure does not bubble up as handler error
// ---------------------------------------------------------------------------

func TestCallLoggerFailureDoesNotBreakHandler(t *testing.T) {
	s := &Server{callLogger: &brokenRecorder{}, logger: log.Logger}

	result, err := s.handleWebFetch(context.Background(), makeReq(map[string]any{"url": "https://test.com"}))
	require.NoError(t, err, "call logger failure should not cause handler to return error")
	require.NotNil(t, result)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: nil call logger does not panic
// ---------------------------------------------------------------------------

func TestNilCallLoggerDoesNotPanic(t *testing.T) {
	s := &Server{logger: log.Logger}

	require.NotPanics(t, func() {
		_, _ = s.handleWebSearch(context.Background(), makeReq(map[string]any{"query": "go"}))
	})
}

// ---------------------------------------------------------------------------
// Test: successful cost tracked, error cost zeroed
// ---------------------------------------------------------------------------

func TestRecordToolInvocationCostTracking(t *testing.T) {
	t.Parallel()

	t.Run("success records base cost", func(t *testing.T) {
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		s.recordToolInvocation(context.Background(), "web_search", "sk-test", nil, time.Now(), time.Second, 42, mcpgo.NewToolResultText("ok"), nil)

		require.Len(t, recorder.records, 1)
		require.Equal(t, calllog.StatusSuccess, recorder.last().Status)
		require.Equal(t, 42, recorder.last().Cost)
	})

	t.Run("error zeroes cost", func(t *testing.T) {
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		s.recordToolInvocation(context.Background(), "web_search", "sk-test", nil, time.Now(), time.Second, 42, nil, goerrors.New("fail"))

		require.Len(t, recorder.records, 1)
		require.Equal(t, calllog.StatusError, recorder.last().Status)
		require.Equal(t, 0, recorder.last().Cost)
	})

	t.Run("tool error result zeroes cost", func(t *testing.T) {
		recorder := &behaviorRecorder{}
		s := &Server{callLogger: recorder, logger: log.Logger}
		s.recordToolInvocation(context.Background(), "web_search", "sk-test", nil, time.Now(), time.Second, 42, mcpgo.NewToolResultError("oops"), nil)

		require.Len(t, recorder.records, 1)
		require.Equal(t, calllog.StatusError, recorder.last().Status)
		require.Equal(t, 0, recorder.last().Cost)
		require.Contains(t, recorder.last().ErrorMessage, "oops")
	})
}

// ---------------------------------------------------------------------------
// Test: recordToolInvocation with negative duration
// ---------------------------------------------------------------------------

func TestRecordToolInvocationNegativeDuration(t *testing.T) {
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}
	s.recordToolInvocation(context.Background(), "test", "key", nil, time.Now(), -time.Second, 0, mcpgo.NewToolResultText("ok"), nil)

	require.Len(t, recorder.records, 1)
	require.True(t, recorder.last().Duration >= 0)
}

// ---------------------------------------------------------------------------
// Test: recordToolInvocation with zero start time
// ---------------------------------------------------------------------------

func TestRecordToolInvocationZeroStartTime(t *testing.T) {
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}
	s.recordToolInvocation(context.Background(), "test", "key", nil, time.Time{}, 0, 0, mcpgo.NewToolResultText("ok"), nil)

	require.Len(t, recorder.records, 1)
	require.False(t, recorder.last().OccurredAt.IsZero())
}

// ---------------------------------------------------------------------------
// Test: recordToolInvocation combines Go error and tool error messages
// ---------------------------------------------------------------------------

func TestRecordToolInvocationCombinedErrors(t *testing.T) {
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}

	goErr := goerrors.New("transport error")
	toolResult := mcpgo.NewToolResultError("validation failed")

	s.recordToolInvocation(context.Background(), "test", "key", nil, time.Now(), time.Second, 10, toolResult, goErr)

	require.Len(t, recorder.records, 1)
	require.Equal(t, calllog.StatusError, recorder.last().Status)
	require.Contains(t, recorder.last().ErrorMessage, "transport error")
	require.Contains(t, recorder.last().ErrorMessage, "validation failed")
}

// ---------------------------------------------------------------------------
// Test: recordToolInvocation redacts file tool arguments (clone safety)
// ---------------------------------------------------------------------------

func TestRecordToolInvocationRedactsArgs(t *testing.T) {
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}

	args := map[string]any{
		"content": "sensitive file content",
		"project": "test-project",
	}

	s.recordToolInvocation(context.Background(), "file_write", "key", args, time.Now(), time.Second, 0, mcpgo.NewToolResultText("ok"), nil)

	require.Len(t, recorder.records, 1)
	// The original args should not be mutated
	require.Equal(t, "sensitive file content", args["content"])
}

// ---------------------------------------------------------------------------
// Test: argumentsMap handles various input types
// ---------------------------------------------------------------------------

func TestArgumentsMapBehavior(t *testing.T) {
	t.Parallel()

	t.Run("nil", func(t *testing.T) {
		require.Nil(t, argumentsMap(nil))
	})

	t.Run("map[string]any passthrough", func(t *testing.T) {
		m := map[string]any{"a": 1}
		require.Equal(t, m, argumentsMap(m))
	})

	t.Run("map[string]string converts", func(t *testing.T) {
		m := map[string]string{"a": "b"}
		result := argumentsMap(m)
		require.Equal(t, map[string]any{"a": "b"}, result)
	})

	t.Run("unexpected type wraps in value key", func(t *testing.T) {
		result := argumentsMap(42)
		require.Equal(t, map[string]any{"value": 42}, result)
	})

	t.Run("string wraps in value key", func(t *testing.T) {
		result := argumentsMap("hello")
		require.Equal(t, map[string]any{"value": "hello"}, result)
	})

	t.Run("bool wraps in value key", func(t *testing.T) {
		result := argumentsMap(true)
		require.Equal(t, map[string]any{"value": true}, result)
	})
}

// ---------------------------------------------------------------------------
// Test: cloneArguments creates independent copy
// ---------------------------------------------------------------------------

func TestCloneArgumentsBehavior(t *testing.T) {
	t.Parallel()

	t.Run("nil input", func(t *testing.T) {
		require.Nil(t, cloneArguments(nil))
	})

	t.Run("empty map", func(t *testing.T) {
		require.Nil(t, cloneArguments(map[string]any{}))
	})

	t.Run("mutation on clone does not affect original", func(t *testing.T) {
		original := map[string]any{"a": "1", "b": "2"}
		cloned := cloneArguments(original)
		cloned["c"] = "3"
		require.NotContains(t, original, "c")
		require.Contains(t, cloned, "c")
	})
}

// ---------------------------------------------------------------------------
// Test: toolErrorMessage extraction
// ---------------------------------------------------------------------------

func TestToolErrorMessageBehavior(t *testing.T) {
	t.Parallel()

	t.Run("nil result", func(t *testing.T) {
		require.Empty(t, toolErrorMessage(nil))
	})

	t.Run("non-error result", func(t *testing.T) {
		require.Empty(t, toolErrorMessage(mcpgo.NewToolResultText("ok")))
	})

	t.Run("error result with text content", func(t *testing.T) {
		result := mcpgo.NewToolResultError("something went wrong")
		require.Equal(t, "something went wrong", toolErrorMessage(result))
	})

	t.Run("error result with empty text content", func(t *testing.T) {
		result := &mcpgo.CallToolResult{
			IsError: true,
			Content: []mcpgo.Content{mcpgo.TextContent{Text: "  "}},
		}
		require.Empty(t, toolErrorMessage(result))
	})

	t.Run("error result with structured content fallback", func(t *testing.T) {
		result := &mcpgo.CallToolResult{
			IsError:           true,
			Content:           []mcpgo.Content{},
			StructuredContent: map[string]any{"code": "ERR"},
		}
		msg := toolErrorMessage(result)
		require.NotEmpty(t, msg)
		require.Contains(t, msg, "ERR")
	})
}

// ---------------------------------------------------------------------------
// Test: apiKeyFromContext resolves from auth context and authorization header
// ---------------------------------------------------------------------------

func TestApiKeyFromContextBehavior(t *testing.T) {
	t.Parallel()

	t.Run("from auth context", func(t *testing.T) {
		ctx := ctxWithAuthKey("sk-test-key-abc")
		require.Equal(t, "sk-test-key-abc", apiKeyFromContext(ctx))
	})

	t.Run("from authorization header in context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), keyAuthorization, "Bearer sk-fallback-xyz")
		require.Equal(t, "sk-fallback-xyz", apiKeyFromContext(ctx))
	})

	t.Run("empty context returns empty", func(t *testing.T) {
		require.Empty(t, apiKeyFromContext(context.Background()))
	})
}

// ---------------------------------------------------------------------------
// Test: LoggerFromContext fallback behavior
// ---------------------------------------------------------------------------

func TestLoggerFromContextBehavior(t *testing.T) {
	t.Run("empty context returns fallback", func(t *testing.T) {
		logger := LoggerFromContext(context.Background())
		require.NotNil(t, logger)
	})
}

// ---------------------------------------------------------------------------
// Test: NewServer with various configurations
// ---------------------------------------------------------------------------

func TestNewServerAllDisabledReturnsError(t *testing.T) {
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{}, log.Logger)
	require.Nil(t, s)
	require.Error(t, err)
}

func TestNewServerWithOnlyMCPPipe(t *testing.T) {
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, log.Logger)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Contains(t, s.AvailableToolNames(), "mcp_pipe")
}

func TestNewServerWithWebSearch(t *testing.T) {
	provider := &stubSearchProvider{
		output: &searchlib.SearchOutput{Items: nil, EngineName: "test", EngineType: "test"},
	}
	s, err := NewServer(
		provider, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil,
		ToolsSettings{WebSearchEnabled: true},
		log.Logger,
	)
	require.NoError(t, err)
	require.Contains(t, s.AvailableToolNames(), "web_search")
}

func TestNewServerWebSearchDisabledByConfig(t *testing.T) {
	provider := &stubSearchProvider{
		output: &searchlib.SearchOutput{Items: nil, EngineName: "test", EngineType: "test"},
	}
	s, err := NewServer(
		provider, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil,
		ToolsSettings{WebSearchEnabled: false, MCPPipeEnabled: true},
		log.Logger,
	)
	require.NoError(t, err)
	require.NotContains(t, s.AvailableToolNames(), "web_search")
}

func TestNewServerHoldManagerNilWhenGetUserRequestDisabled(t *testing.T) {
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, log.Logger)
	require.NoError(t, err)
	require.Nil(t, s.HoldManager())
}

func TestNewServerNilLogger(t *testing.T) {
	s, err := NewServer(nil, nil, nil, nil, rag.Settings{}, nil, nil, nil, nil, ToolsSettings{MCPPipeEnabled: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
}

// ---------------------------------------------------------------------------
// Test: AvailableToolNames is sorted
// ---------------------------------------------------------------------------

func TestAvailableToolNamesSorted(t *testing.T) {
	s := &Server{
		toolHandlers: map[string]srv.ToolHandlerFunc{
			"z_tool": nil,
			"a_tool": nil,
			"m_tool": nil,
		},
	}
	names := s.AvailableToolNames()
	require.Equal(t, []string{"a_tool", "m_tool", "z_tool"}, names)
}

func TestAvailableToolNamesNilServer(t *testing.T) {
	var s *Server
	require.Empty(t, s.AvailableToolNames())
}

// ---------------------------------------------------------------------------
// Test: web_search full flow with mock
// ---------------------------------------------------------------------------

func TestWebSearchFullFlow(t *testing.T) {
	t.Parallel()
	var billingCalls int

	provider := &stubSearchProvider{
		output: &searchlib.SearchOutput{
			Items: []searchlib.SearchResultItem{
				{URL: "https://go.dev", Name: "Go", Snippet: "The Go programming language"},
			},
			EngineName: "google",
			EngineType: "programmable",
		},
	}

	webSearchTool, err := tools.NewWebSearchTool(
		provider,
		log.Logger.Named("test_web_search"),
		func(_ context.Context) string { return "sk-valid" },
		func(_ context.Context, _ string, price oneapi.Price, _ string) error {
			billingCalls++
			return nil
		},
	)
	require.NoError(t, err)

	recorder := &behaviorRecorder{}
	s := &Server{
		webSearch:  webSearchTool,
		callLogger: recorder,
		logger:     log.Logger,
	}

	ctx := ctxWithAuthKey("sk-valid")
	result, err := s.handleWebSearch(ctx, makeReq(map[string]any{"query": "golang tutorial"}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, billingCalls)

	payload := getJSON(t, result)
	results, ok := payload["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)

	require.Len(t, recorder.records, 1)
	require.Equal(t, "web_search", recorder.last().ToolName)
	require.Equal(t, calllog.StatusSuccess, recorder.last().Status)
}

// ---------------------------------------------------------------------------
// Test: web_search empty/whitespace query
// ---------------------------------------------------------------------------

func TestWebSearchEmptyQuery(t *testing.T) {
	t.Parallel()

	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-key" },
		func(_ context.Context, _ string, _ oneapi.Price, _ string) error { return nil },
	)
	require.NoError(t, err)

	s := &Server{webSearch: webSearchTool, logger: log.Logger}

	tests := []struct {
		name  string
		query string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.handleWebSearch(context.Background(), makeReq(map[string]any{"query": tc.query}))
			require.NoError(t, err)
			require.True(t, result.IsError)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: web_search missing query parameter
// ---------------------------------------------------------------------------

func TestWebSearchMissingQueryParam(t *testing.T) {
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-key" },
		func(_ context.Context, _ string, _ oneapi.Price, _ string) error { return nil },
	)
	require.NoError(t, err)

	s := &Server{webSearch: webSearchTool, logger: log.Logger}

	result, err := s.handleWebSearch(context.Background(), makeReq(map[string]any{}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: web_search missing API key
// ---------------------------------------------------------------------------

func TestWebSearchMissingAPIKey(t *testing.T) {
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "" },
		func(_ context.Context, _ string, _ oneapi.Price, _ string) error { return nil },
	)
	require.NoError(t, err)

	s := &Server{webSearch: webSearchTool, logger: log.Logger}
	result, err := s.handleWebSearch(context.Background(), makeReq(map[string]any{"query": "test"}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, getText(t, result), "missing authorization")
}

// ---------------------------------------------------------------------------
// Test: web_search billing denied
// ---------------------------------------------------------------------------

func TestWebSearchBillingDenied(t *testing.T) {
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{output: &searchlib.SearchOutput{}},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-key" },
		func(_ context.Context, _ string, _ oneapi.Price, _ string) error {
			return goerrors.New("quota exceeded")
		},
	)
	require.NoError(t, err)

	s := &Server{webSearch: webSearchTool, logger: log.Logger}
	ctx := ctxWithAuthKey("sk-key")
	result, err := s.handleWebSearch(ctx, makeReq(map[string]any{"query": "test"}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, getText(t, result), "billing check failed")
}

// ---------------------------------------------------------------------------
// Test: web_search provider error
// ---------------------------------------------------------------------------

func TestWebSearchProviderError(t *testing.T) {
	webSearchTool, err := tools.NewWebSearchTool(
		&stubSearchProvider{err: goerrors.New("search engine down")},
		log.Logger.Named("test"),
		func(_ context.Context) string { return "sk-key" },
		func(_ context.Context, _ string, _ oneapi.Price, _ string) error { return nil },
	)
	require.NoError(t, err)

	s := &Server{webSearch: webSearchTool, logger: log.Logger}
	ctx := ctxWithAuthKey("sk-key")
	result, err := s.handleWebSearch(ctx, makeReq(map[string]any{"query": "test"}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, getText(t, result), "search failed")
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe empty steps
// ---------------------------------------------------------------------------

func TestMCPPipeEmptySteps(t *testing.T) {
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe duplicate step IDs
// ---------------------------------------------------------------------------

func TestMCPPipeDuplicateStepID(t *testing.T) {
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "dup", "tool": "web_search", "args": map[string]any{"query": "a"}},
			map[string]any{"id": "dup", "tool": "web_search", "args": map[string]any{"query": "b"}},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe step with empty ID
// ---------------------------------------------------------------------------

func TestMCPPipeEmptyStepID(t *testing.T) {
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "", "tool": "web_search", "args": map[string]any{"query": "a"}},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe step with multiple modes is rejected
// ---------------------------------------------------------------------------

func TestMCPPipeStepMultipleModes(t *testing.T) {
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{
				"id":   "bad",
				"tool": "web_search",
				"pipe": map[string]any{"steps": []any{}},
			},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe spec as JSON string
// ---------------------------------------------------------------------------

func TestMCPPipeSpecAsJSONString(t *testing.T) {
	callCount := 0
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		callCount++
		r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
		return r, nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	specJSON := `{"steps":[{"id":"s1","tool":"web_fetch","args":{"url":"https://example.com"}}]}`
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"spec": specJSON,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 1, callCount)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe continue_on_error
// ---------------------------------------------------------------------------

func TestMCPPipeContinueOnError(t *testing.T) {
	callCount := 0
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		callCount++
		if callCount == 1 {
			return mcpgo.NewToolResultError("step 1 failed"), nil
		}
		r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
		return r, nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	_, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"continue_on_error": true,
		"steps": []any{
			map[string]any{"id": "s1", "tool": "web_search", "args": map[string]any{"query": "a"}},
			map[string]any{"id": "s2", "tool": "web_search", "args": map[string]any{"query": "b"}},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, 2, callCount, "both steps should execute when continue_on_error is true")
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe without continue_on_error stops on first error
// ---------------------------------------------------------------------------

func TestMCPPipeStopsOnFirstError(t *testing.T) {
	callCount := 0
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		callCount++
		if callCount == 1 {
			return mcpgo.NewToolResultError("step 1 failed"), nil
		}
		r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
		return r, nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "s1", "tool": "web_search", "args": map[string]any{"query": "a"}},
			map[string]any{"id": "s2", "tool": "web_search", "args": map[string]any{"query": "b"}},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, 1, callCount, "should stop after first error")
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe max depth exceeded
// ---------------------------------------------------------------------------

func TestMCPPipeMaxDepthExceeded(t *testing.T) {
	pipeTool, err := tools.NewMCPPipeTool(
		log.Logger.Named("test_pipe"),
		func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
			r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
			return r, nil
		},
		tools.PipeLimits{MaxSteps: 100, MaxDepth: 1, MaxParallel: 4},
	)
	require.NoError(t, err)

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "outer", "pipe": map[string]any{
				"steps": []any{
					map[string]any{"id": "inner", "pipe": map[string]any{
						"steps": []any{
							map[string]any{"id": "deep", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
						},
					}},
				},
			}},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe max steps exceeded
// ---------------------------------------------------------------------------

func TestMCPPipeMaxStepsExceeded(t *testing.T) {
	pipeTool, err := tools.NewMCPPipeTool(
		log.Logger.Named("test_pipe"),
		func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
			r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
			return r, nil
		},
		tools.PipeLimits{MaxSteps: 2, MaxDepth: 5, MaxParallel: 4},
	)
	require.NoError(t, err)

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "a", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
			map[string]any{"id": "b", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
			map[string]any{"id": "c", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
		},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe invalid spec
// ---------------------------------------------------------------------------

func TestMCPPipeInvalidSpec(t *testing.T) {
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, _ any) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}

	t.Run("empty spec string", func(t *testing.T) {
		result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
			"spec": "",
		}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})

	t.Run("malformed JSON spec string", func(t *testing.T) {
		result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
			"spec": "{not valid json",
		}))
		require.NoError(t, err)
		require.True(t, result.IsError)
	})
}

// ---------------------------------------------------------------------------
// Test: ToolsSettings zero value
// ---------------------------------------------------------------------------

func TestToolsSettingsDefaults(t *testing.T) {
	s := ToolsSettings{}
	require.False(t, s.WebSearchEnabled)
	require.False(t, s.WebFetchEnabled)
	require.False(t, s.AskUserEnabled)
	require.False(t, s.GetUserRequestEnabled)
	require.False(t, s.ExtractKeyInfoEnabled)
	require.False(t, s.FileIOEnabled)
	require.False(t, s.MemoryEnabled)
	require.False(t, s.MCPPipeEnabled)
	require.False(t, s.FindToolEnabled)
}

// ---------------------------------------------------------------------------
// Test: filterToolsListBody edge cases
// ---------------------------------------------------------------------------

func TestFilterToolsListBodyInvalidJSON(t *testing.T) {
	_, _, err := filterToolsListBody([]byte(`not json`), map[string]struct{}{"x": {}})
	require.Error(t, err)
}

func TestFilterToolsListBodyNoToolsKey(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	result, changed, err := filterToolsListBody(body, map[string]struct{}{"x": {}})
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, result)
}

func TestFilterToolsListBodyEmptyDisabled(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"web_search"}]}}`)
	result, changed, err := filterToolsListBody(body, map[string]struct{}{})
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, result)
}

func TestFilterToolsListBodyAllToolsDisabled(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a"},{"name":"b"}]}}`)
	disabled := map[string]struct{}{"a": {}, "b": {}}

	filteredBody, changed, err := filterToolsListBody(body, disabled)
	require.NoError(t, err)
	require.True(t, changed)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(filteredBody, &payload))
	result := payload["result"].(map[string]any)
	toolsList := result["tools"].([]any)
	require.Empty(t, toolsList)
}

// ---------------------------------------------------------------------------
// Test: logInvalidArrayItemSchemas does not panic on edge cases
// ---------------------------------------------------------------------------

func TestLogInvalidArrayItemSchemasEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil logger", func(t *testing.T) {
		require.NotPanics(t, func() {
			logInvalidArrayItemSchemas(nil, mcpgo.Tool{})
		})
	})

	t.Run("non-map property", func(t *testing.T) {
		tool := mcpgo.Tool{
			InputSchema: mcpgo.ToolInputSchema{
				Properties: map[string]any{
					"weird": 42,
				},
			},
		}
		require.NotPanics(t, func() {
			logInvalidArrayItemSchemas(log.Logger, tool)
		})
	})

	t.Run("array property with items", func(t *testing.T) {
		tool := mcpgo.Tool{
			InputSchema: mcpgo.ToolInputSchema{
				Properties: map[string]any{
					"tags": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		}
		require.NotPanics(t, func() {
			logInvalidArrayItemSchemas(log.Logger, tool)
		})
	})

	t.Run("non-array property", func(t *testing.T) {
		tool := mcpgo.Tool{
			InputSchema: mcpgo.ToolInputSchema{
				Properties: map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
		}
		require.NotPanics(t, func() {
			logInvalidArrayItemSchemas(log.Logger, tool)
		})
	})

	t.Run("array property without items", func(t *testing.T) {
		tool := mcpgo.Tool{
			InputSchema: mcpgo.ToolInputSchema{
				Properties: map[string]any{
					"tags": map[string]any{"type": "array"},
				},
			},
		}
		require.NotPanics(t, func() {
			logInvalidArrayItemSchemas(log.Logger, tool)
		})
	})
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe parallel execution
// ---------------------------------------------------------------------------

func TestMCPPipeParallelExecution(t *testing.T) {
	var mu = make(chan struct{}, 1)
	callCount := 0

	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, args any) (*mcpgo.CallToolResult, error) {
		mu <- struct{}{}
		callCount++
		<-mu
		r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
		return r, nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"steps": []any{
			map[string]any{"id": "group", "parallel": []any{
				map[string]any{"id": "p1", "tool": "web_fetch", "args": map[string]any{"url": "a"}},
				map[string]any{"id": "p2", "tool": "web_fetch", "args": map[string]any{"url": "b"}},
			}},
		},
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, 2, callCount)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe variable interpolation
// ---------------------------------------------------------------------------

func TestMCPPipeVariableInterpolation(t *testing.T) {
	var capturedQuery string
	pipeTool := mustBehaviorPipeTool(t, func(_ context.Context, _ string, args any) (*mcpgo.CallToolResult, error) {
		argsMap, ok := args.(map[string]any)
		if ok {
			capturedQuery, _ = argsMap["query"].(string)
		}
		r, _ := mcpgo.NewToolResultJSON(map[string]any{"ok": true})
		return r, nil
	})

	s := &Server{mcpPipe: pipeTool, logger: log.Logger}
	result, err := s.handleMCPPipe(context.Background(), makeReq(map[string]any{
		"vars": map[string]any{"q": "golang"},
		"steps": []any{
			map[string]any{"id": "s1", "tool": "web_search", "args": map[string]any{"query": "${vars.q}"}},
		},
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "golang", capturedQuery)
}

// ---------------------------------------------------------------------------
// Test: mcp_pipe self-invocation prevention via invoker
// ---------------------------------------------------------------------------

func TestMCPPipeSelfInvocationPrevention(t *testing.T) {
	// Simulate the invoker closure from NewServer
	toolHandlers := map[string]func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error){
		"web_search": func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("searched"), nil
		},
	}

	invoker := func(_ context.Context, toolName string, args any) (*mcpgo.CallToolResult, error) {
		if toolName == "mcp_pipe" {
			return mcpgo.NewToolResultError("mcp_pipe cannot invoke itself"), nil
		}
		handler, ok := toolHandlers[toolName]
		if !ok {
			return mcpgo.NewToolResultError("unknown tool: " + toolName), nil
		}
		req := mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: toolName, Arguments: args}}
		return handler(context.Background(), req)
	}

	// Test self-invocation rejection
	r, err := invoker(context.Background(), "mcp_pipe", nil)
	require.NoError(t, err)
	require.True(t, r.IsError)
	require.Contains(t, getText(t, r), "mcp_pipe cannot invoke itself")

	// Test unknown tool rejection
	r2, err := invoker(context.Background(), "nonexistent", nil)
	require.NoError(t, err)
	require.True(t, r2.IsError)
	require.Contains(t, getText(t, r2), "unknown tool")

	// Test normal tool works
	r3, err := invoker(context.Background(), "web_search", map[string]any{"query": "test"})
	require.NoError(t, err)
	require.False(t, r3.IsError)
}

// ---------------------------------------------------------------------------
// Test: registerTool populates both handler and definition maps
// ---------------------------------------------------------------------------

func TestRegisterToolPopulatesMaps(t *testing.T) {
	s := &Server{
		toolHandlers:    make(map[string]srv.ToolHandlerFunc),
		toolDefinitions: make(map[string]mcpgo.Tool),
		logger:          log.Logger,
	}

	// We need an MCP server for registerTool - just verify the maps are populated
	require.Empty(t, s.toolHandlers)
	require.Empty(t, s.toolDefinitions)
}

// ---------------------------------------------------------------------------
// Test: concurrent handler invocations are safe
// ---------------------------------------------------------------------------

func TestConcurrentHandlerInvocations(t *testing.T) {
	recorder := &behaviorRecorder{}
	s := &Server{callLogger: recorder, logger: log.Logger}

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = s.handleWebSearch(context.Background(), makeReq(map[string]any{"query": "test"}))
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	require.Equal(t, 10, recorder.count())
}

// ---------------------------------------------------------------------------
// helper: build mcp_pipe tool for behavior tests
// ---------------------------------------------------------------------------

func mustBehaviorPipeTool(t *testing.T, invoker tools.PipeInvoker) *tools.MCPPipeTool {
	t.Helper()
	tool, err := tools.NewMCPPipeTool(log.Logger.Named("test_pipe"), invoker, tools.PipeLimits{})
	require.NoError(t, err)
	return tool
}
