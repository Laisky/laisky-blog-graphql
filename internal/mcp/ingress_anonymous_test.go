package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// anonymousAuthorizations are the request shapes that carry no credential.
var anonymousAuthorizations = []string{"", "Bearer", "Bearer   "}

// TestIngressRefusesAnonymousToolExecution reproduces review finding A of
// PR #95: a request without credentials reached the SDK for every method, so
// an anonymous tools/call ran mcp_pipe, its steps and the call-log write. The
// transport must answer it 401 with a Bearer challenge before the SDK
// dispatches, on the modern stateless path and inside a legacy session.
func TestIngressRefusesAnonymousToolExecution(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture)
	audit := &behaviorRecorder{}
	s.callLogger = audit
	handler := s.Handler()

	for _, authorization := range anonymousAuthorizations {
		t.Run(fmt.Sprintf("modern stateless %q", authorization), func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", authorization))
			requireAuthenticationRequired(t, response)
		})
		t.Run(fmt.Sprintf("legacy session %q", authorization), func(t *testing.T) {
			session := initializeReviewSession(t, handler, mcpgo.ProtocolVersion20250618, authorization)
			response := sendSDKWireRequest(t, handler, reviewSessionMessage("tools/call"), session)
			requireAuthenticationRequired(t, response)
			require.Empty(t, response.Header().Get(mcpgo.HeaderSessionID))
		})
	}

	require.Zero(t, effects.Load(), "an anonymous request must never reach a tool handler")
	require.Zero(t, audit.count(), "an anonymous request must never write a call-log row")
	require.Zero(t, fixture.calls.Load(), "an anonymous request never consults the authority")

	t.Run("the same call with an issued key still runs", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, newModernToolCallRequest(t, context.Background(), "/mcp/", "Bearer "+issuedIngressKey))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "tenant effect")
		require.Equal(t, int64(1), effects.Load())
	})
}

// TestIngressAnonymousMethodPolicy pins the anonymous allowlist: discovery
// requests and the legacy handshake's notifications/initialized pass to the
// SDK; every other method or notification, a client response, a batch and any
// body the policy cannot classify is refused before the SDK sees it, including
// bodies that exploit encoding/json quirks.
func TestIngressAnonymousMethodPolicy(t *testing.T) {
	fixture := newOneAPITokenFixture(t, issuedIngressKey)
	s, effects := newIngressTestServer(t, fixture)
	handler := s.Handler()

	discovery := map[string]string{
		"initialize": `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
			`"capabilities":{},"clientInfo":{"name":"anonymous-policy","version":"1"}}}`,
		"ping":                      `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		"tools/list":                `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"server/discover":           `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
		"prompts/list":              `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`,
		"resources/list":            `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		"resources/templates/list":  `{"jsonrpc":"2.0","id":1,"method":"resources/templates/list"}`,
		"notifications/initialized": `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
	for name, body := range discovery {
		t.Run("discovery "+name, func(t *testing.T) {
			response := sendRawAnonymous(t, handler, body)
			require.NotEqual(t, http.StatusUnauthorized, response.Code, response.Body.String())
			require.Empty(t, response.Header().Get("WWW-Authenticate"))
		})
	}

	refused := map[string]string{
		"tools/call":           `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mcp_pipe","arguments":{}}}`,
		"tasks/get":            `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"t"}}`,
		"tasks/result":         `{"jsonrpc":"2.0","id":1,"method":"tasks/result","params":{"taskId":"t"}}`,
		"tasks/list":           `{"jsonrpc":"2.0","id":1,"method":"tasks/list"}`,
		"tasks/cancel":         `{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"taskId":"t"}}`,
		"completion/complete":  `{"jsonrpc":"2.0","id":1,"method":"completion/complete"}`,
		"resources/read":       `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///x"}}`,
		"resources/subscribe":  `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"uri":"file:///x"}}`,
		"prompts/get":          `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"x"}}`,
		"logging/setLevel":     `{"jsonrpc":"2.0","id":1,"method":"logging/setLevel","params":{"level":"debug"}}`,
		"subscriptions/listen": `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen"}`,
		"unknown method":       `{"jsonrpc":"2.0","id":1,"method":"x-vendor/run"}`,
		"client response":      `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`,
		"notification with id": `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`,
		"cancellation":         `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		"progress":             `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":1}}`,
		"roots changed":        `{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`,
		"batch":                `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"tools/call"}]`,
		"duplicate method key": `{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/call","params":{"name":"mcp_pipe"}}`,
		"case-folded key":      `{"jsonrpc":"2.0","id":1,"METHOD":"tools/call","params":{"name":"mcp_pipe"}}`,
		"not json":             `tools/call`,
		"empty body":           ``,
		"json null":            `null`,
	}
	for name, body := range refused {
		t.Run("refused "+name, func(t *testing.T) {
			requireAuthenticationRequired(t, sendRawAnonymous(t, handler, body))
		})
	}

	t.Run("bodyless legacy stream and delete keep reaching the SDK", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			request := httptest.NewRequest(method, "/mcp/", nil)
			request.Header.Set("Accept", "text/event-stream")
			request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20250618)
			response := httptest.NewRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // an SSE GET would otherwise stream until the client leaves
			handler.ServeHTTP(response, request.WithContext(ctx))
			require.NotEqual(t, http.StatusUnauthorized, response.Code, method)
		}
	})

	require.Zero(t, effects.Load())
	require.Zero(t, fixture.calls.Load())
}

// TestToolDispatchRequiresVerifiedCallerForEveryTool is the dispatch-level
// half of the anonymous policy: every tool registered with the SDK, called
// through the SDK's own dispatcher with the transport gate bypassed, refuses
// a caller without a verified API key before its handler, audit row or
// billing report runs, and still serves a verified one.
func TestToolDispatchRequiresVerifiedCallerForEveryTool(t *testing.T) {
	s := newServerWithEveryConstructibleTool(t)
	audit := &behaviorRecorder{}
	s.callLogger = audit
	reports := 0
	s.billingReporter = func(context.Context, string, oneapi.Price, string) error {
		reports++
		return nil
	}

	registered := s.sdk.ListTools()
	require.GreaterOrEqual(t, len(registered), 10, "the matrix must cover the real catalog")
	// The SDK does not wrap task tools (AddTaskTool) with tool middleware, and
	// tools added outside registerTool would escape this matrix: the
	// advertised catalog must be exactly registerTool's.
	listing, err := json.Marshal(s.sdk.HandleMessage(contextWithVerified(authtest.Verified("sk-dispatch-list")),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	require.NoError(t, err)
	var catalog struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(listing, &catalog), string(listing))
	advertised := make([]string, 0, len(catalog.Result.Tools))
	for _, tool := range catalog.Result.Tools {
		advertised = append(advertised, tool.Name)
	}
	require.ElementsMatch(t, s.AvailableToolNames(), advertised)
	require.Len(t, registered, len(advertised))
	for name := range registered {
		raw := s.sdk.HandleMessage(context.Background(), toolCallMessage(t, name))
		encoded, err := json.Marshal(raw)
		require.NoError(t, err)
		var payload struct {
			Result mcpgo.CallToolResult `json:"result"`
		}
		require.NoError(t, json.Unmarshal(encoded, &payload), string(encoded))
		require.True(t, payload.Result.IsError, "%s: %s", name, encoded)
		require.Contains(t, string(encoded), authenticationRequiredDescription, name)
	}
	require.Zero(t, audit.count(), "no tool handler may run for an unverified caller")
	require.Zero(t, reports)

	verified := authtest.Verified("sk-dispatch-verified")
	raw := s.sdk.HandleMessage(contextWithVerified(verified), toolCallMessage(t, "mcp_pipe"))
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), authenticationRequiredDescription)
	require.Equal(t, 1, audit.count(), "a verified caller still reaches the handler")
}

// sendRawAnonymous posts body without credentials.
func sendRawAnonymous(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp/", bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20250618)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// requireAuthenticationRequired asserts the RFC 6750 §3.1 challenge for a
// request that presented no credential: 401, a Bearer challenge with only the
// RFC 9728 metadata pointer (no error code), and no tool output.
func requireAuthenticationRequired(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	challenge := response.Header().Get("WWW-Authenticate")
	require.Equal(t, `Bearer resource_metadata="https://mcp.laisky.com/.well-known/oauth-protected-resource"`, challenge)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), response.Body.String())
	require.Equal(t, "authentication_required", body["error"])
	require.NotContains(t, response.Body.String(), "tenant effect")
	require.False(t, strings.Contains(response.Body.String(), "anonymous step"))
}

// newServerWithEveryConstructibleTool registers every tool whose dependency a
// unit test can build in memory: web_search, ask_user, get_user_request, the
// FileIO family with history, mcp_pipe and find_tool.
func newServerWithEveryConstructibleTool(t *testing.T) *Server {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:dispatch_every_tool?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	askUserService, err := askuser.NewService(db, glog.Shared)
	require.NoError(t, err)
	userRequests := newUserPreferenceServiceForToolsListTest(t, "file:dispatch_every_tool_requests?mode=memory&cache=shared")
	adapter, err := ragplugin.New(&files.Service{})
	require.NoError(t, err)
	provider := &stubSearchProvider{output: &searchlib.SearchOutput{EngineName: "test", EngineType: "test"}}

	s, err := NewServer(provider, askUserService, userRequests, nil,
		rag.Settings{OpenAIBaseURL: "http://127.0.0.1:1", EmbeddingModel: "dispatch-test"}, adapter, nil, nil, nil,
		ToolsSettings{WebSearchEnabled: true, AskUserEnabled: true, GetUserRequestEnabled: true, FileIOEnabled: true,
			MCPPipeEnabled: true, FindToolEnabled: true},
		glog.Shared, WithFileHistoryReader(&discoveryOnlyHistory{}))
	require.NoError(t, err)
	return s
}

// contextWithVerified returns a background context carrying verified.
func contextWithVerified(verified *mcpauth.Context) context.Context {
	return mcpauth.WithContext(context.Background(), verified)
}

// toolCallMessage builds a raw JSON-RPC tools/call for name without arguments.
func toolCallMessage(t *testing.T, name string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": map[string]any{}}})
	require.NoError(t, err)
	return body
}
