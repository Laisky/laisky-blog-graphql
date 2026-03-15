package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// deterministicEmbedder returns embeddings based on simple keyword heuristics
// so tests can verify ranking without a real embedding service.
type deterministicEmbedder struct{}

func (d *deterministicEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vecs := make([]pgvector.Vector, len(inputs))
	for i, text := range inputs {
		vecs[i] = simpleVec(text)
	}
	return vecs, nil
}

// simpleVec creates a deterministic 8-dimension vector from text
// by hashing each character position. This is enough to make
// cosine similarity distinguish "search" queries from "file" queries.
func simpleVec(text string) pgvector.Vector {
	dims := 8
	vals := make([]float32, dims)
	for i, ch := range text {
		vals[(i)%dims] += float32(ch)
	}
	// normalize
	var norm float64
	for _, v := range vals {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		n := float32(math.Sqrt(norm))
		for i := range vals {
			vals[i] /= n
		}
	}
	return pgvector.NewVector(vals)
}

func testSettings() rag.Settings {
	return rag.Settings{
		SemanticWeight: 0.65,
		LexicalWeight:  0.35,
		EmbeddingModel: "test",
		OpenAIBaseURL:  "http://localhost",
	}
}

func sampleTools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool("web_search",
			mcp.WithDescription("Search the web using a search engine."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		),
		mcp.NewTool("web_fetch",
			mcp.WithDescription("Fetch a web page and return its content."),
			mcp.WithString("url", mcp.Required(), mcp.Description("URL to fetch")),
		),
		mcp.NewTool("file_read",
			mcp.WithDescription("Read the contents of a file from disk."),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path")),
		),
		mcp.NewTool("file_write",
			mcp.WithDescription("Write content to a file on disk."),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path")),
			mcp.WithString("content", mcp.Required(), mcp.Description("Content to write")),
		),
		mcp.NewTool("file_delete",
			mcp.WithDescription("Delete a file or directory."),
			mcp.WithString("path", mcp.Required(), mcp.Description("File path")),
		),
		mcp.NewTool("memory_before_turn",
			mcp.WithDescription("Retrieve memory context before processing a conversation turn."),
		),
	}
}

func mustFindToolTool(t *testing.T) *FindToolTool {
	t.Helper()
	tool, err := NewFindToolTool(
		&deterministicEmbedder{},
		log.Logger.Named("find_tool_test"),
		func(ctx context.Context) string { return "Bearer task@sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		testSettings(),
	)
	require.NoError(t, err)
	tool.SetTools(sampleTools())
	return tool
}

func TestNewFindToolTool_Validation(t *testing.T) {
	settings := testSettings()
	header := func(ctx context.Context) string { return "Bearer sk-test" }
	billing := func(context.Context, string, oneapi.Price, string) error { return nil }
	logger := log.Logger.Named("test")

	_, err := NewFindToolTool(nil, logger, header, billing, settings)
	require.Error(t, err, "nil embedder")

	_, err = NewFindToolTool(&deterministicEmbedder{}, nil, header, billing, settings)
	require.Error(t, err, "nil logger")

	_, err = NewFindToolTool(&deterministicEmbedder{}, logger, nil, billing, settings)
	require.Error(t, err, "nil header provider")

	_, err = NewFindToolTool(&deterministicEmbedder{}, logger, header, nil, settings)
	require.Error(t, err, "nil billing checker")
}

// TestFindToolTool_HandleSuccess verifies the default (auto) mode returns tools.
func TestFindToolTool_HandleSuccess(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "search the web"},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Parse response and verify structure.
	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	require.LessOrEqual(t, len(tools), findToolDefaultTopK)

	// Each result should have name, description, inputSchema.
	first := tools[0].(map[string]any)
	require.Contains(t, first, "name")
	require.Contains(t, first, "description")
	require.Contains(t, first, "inputSchema")
}

func TestFindToolTool_EmptyQuery(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": ""},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestFindToolTool_ReturnsAtMostTopK(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "file operations read write delete"},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.LessOrEqual(t, len(tools), findToolDefaultTopK)
}

func TestFindToolTool_Definition(t *testing.T) {
	tool := mustFindToolTool(t)
	def := tool.Definition()
	require.Equal(t, "find_tool", def.Name)
	require.NotEmpty(t, def.Description)
	require.Contains(t, def.InputSchema.Properties, "query")
	require.Contains(t, def.InputSchema.Properties, "mode")
	require.Contains(t, def.InputSchema.Properties, "return_references_only")
}

func TestFindToolTool_SetToolsResetsIndex(t *testing.T) {
	tool := mustFindToolTool(t)

	// First query to build the index (use embedding mode to trigger index build).
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "search", "mode": "embedding"},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	// SetTools resets the cache.
	tool.SetTools([]mcp.Tool{
		mcp.NewTool("only_tool", mcp.WithDescription("The only tool.")),
	})

	result, err = tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.Len(t, tools, 1)
	require.Equal(t, "only_tool", tools[0].(map[string]any)["name"])
}

func TestBuildToolDocument(t *testing.T) {
	tool := mcp.NewTool("web_search",
		mcp.WithDescription("Search the web."),
		mcp.WithString("query", mcp.Required()),
	)
	doc := buildToolDocument(tool)
	require.Contains(t, doc, "Tool: web_search")
	require.Contains(t, doc, "Description: Search the web.")
	require.Contains(t, doc, "Parameters:")
}

// --- Regex mode tests ---

func TestFindToolTool_RegexMode(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "file_.*",
			"mode":  "regex",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	// All matched tools should have "file_" prefix.
	for _, raw := range tools {
		m := raw.(map[string]any)
		name := m["name"].(string)
		require.Contains(t, name, "file_")
	}
}

func TestFindToolTool_RegexMode_CaseInsensitive(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "WEB",
			"mode":  "regex",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	// Should match web_search and web_fetch.
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	require.Contains(t, names, "web_search")
	require.Contains(t, names, "web_fetch")
}

func TestFindToolTool_RegexMode_InvalidPattern(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "[invalid",
			"mode":  "regex",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestFindToolTool_RegexMode_PatternTooLong(t *testing.T) {
	tool := mustFindToolTool(t)

	longPattern := ""
	for i := 0; i < 201; i++ {
		longPattern += "a"
	}
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": longPattern,
			"mode":  "regex",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// --- BM25 mode tests ---

func TestFindToolTool_BM25Mode(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "search web engine",
			"mode":  "bm25",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	// "web_search" should rank highest for "search web engine".
	first := tools[0].(map[string]any)
	require.Equal(t, "web_search", first["name"])
}

func TestFindToolTool_BM25Mode_FileQuery(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "read file contents from disk",
			"mode":  "bm25",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	// "file_read" should rank highly for "read file contents from disk".
	first := tools[0].(map[string]any)
	require.Equal(t, "file_read", first["name"])
}

// --- Embedding mode tests ---

func TestFindToolTool_EmbeddingMode(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "search the web",
			"mode":  "embedding",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)
	require.LessOrEqual(t, len(tools), findToolDefaultTopK)
}

// --- Auto mode tests ---

func TestFindToolTool_AutoMode_DetectsRegex(t *testing.T) {
	tool := mustFindToolTool(t)

	// Query with regex metacharacters should auto-detect regex mode.
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "file_.*",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	// All results should be file_ tools.
	for _, raw := range tools {
		m := raw.(map[string]any)
		name := m["name"].(string)
		require.Contains(t, name, "file_")
	}
}

func TestFindToolTool_AutoMode_DetectsToolNamePattern(t *testing.T) {
	tool := mustFindToolTool(t)

	// Query that looks like a tool name (has underscore, no spaces).
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "web_search",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)

	first := tools[0].(map[string]any)
	require.Equal(t, "web_search", first["name"])
}

func TestFindToolTool_AutoMode_FallsBM25(t *testing.T) {
	tool := mustFindToolTool(t)

	// Natural language query should use BM25.
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "I want to search the web",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	tools := payload["tools"].([]any)
	require.NotEmpty(t, tools)
}

// --- return_references_only tests ---

func TestFindToolTool_ReturnReferencesOnly(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query":                  "web",
			"mode":                   "regex",
			"return_references_only": true,
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))

	// Should have tool_references key, not tools key.
	refs, ok := payload["tool_references"].([]any)
	require.True(t, ok, "response should contain tool_references key")
	require.NotEmpty(t, refs)

	// Each reference should have type and tool_name.
	for _, raw := range refs {
		ref := raw.(map[string]any)
		require.Equal(t, "tool_reference", ref["type"])
		require.NotEmpty(t, ref["tool_name"])
	}

	// Should NOT contain full schemas.
	for _, raw := range refs {
		ref := raw.(map[string]any)
		require.NotContains(t, ref, "description")
		require.NotContains(t, ref, "inputSchema")
	}
}

func TestFindToolTool_ReturnReferencesOnly_False(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query":                  "web",
			"mode":                   "regex",
			"return_references_only": false,
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	text := toolResultText(result)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload))

	// Should return full schemas under tools key.
	tools, ok := payload["tools"].([]any)
	require.True(t, ok, "response should contain tools key")
	require.NotEmpty(t, tools)

	first := tools[0].(map[string]any)
	require.Contains(t, first, "name")
	require.Contains(t, first, "description")
	require.Contains(t, first, "inputSchema")
}

// --- Invalid mode test ---

func TestFindToolTool_InvalidMode(t *testing.T) {
	tool := mustFindToolTool(t)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "test",
			"mode":  "invalid_mode",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

// --- detectSearchMode unit tests ---

func TestDetectSearchMode(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"file_.*", FindToolModeRegex},
		{"web_search", FindToolModeRegex},
		{"file_read", FindToolModeRegex},
		{"(?i)web", FindToolModeRegex},
		{"search|fetch", FindToolModeRegex},
		{"I want to search the web", FindToolModeBM25},
		{"read a file from disk", FindToolModeBM25},
		{"memory context", FindToolModeBM25},
		{"hello world", FindToolModeBM25},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := detectSearchMode(tt.query)
			require.Equal(t, tt.expected, got)
		})
	}
}

// --- Embedding fallback tests ---

// failingEmbedder always returns an error, simulating a broken embeddings endpoint.
type failingEmbedder struct{}

func (d *failingEmbedder) EmbedTexts(_ context.Context, _ string, _ []string) ([]pgvector.Vector, error) {
	return nil, fmt.Errorf("embeddings endpoint status 404, url=http://localhost/v1/embeddings, body=not found")
}

func mustFindToolToolWithEmbedder(t *testing.T, embedder rag.Embedder) *FindToolTool {
	t.Helper()
	tool, err := NewFindToolTool(
		embedder,
		log.Logger.Named("find_tool_test"),
		func(ctx context.Context) string { return "Bearer task@sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		testSettings(),
	)
	require.NoError(t, err)
	tool.SetTools(sampleTools())
	return tool
}

// TestFindToolTool_EmbeddingFailureReturnsError verifies that when the embedding
// endpoint fails, the error is surfaced to the caller (not silently swallowed).
func TestFindToolTool_EmbeddingFailureReturnsError(t *testing.T) {
	tool := mustFindToolToolWithEmbedder(t, &failingEmbedder{})

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{
			"query": "search the web",
			"mode":  "embedding",
		},
	}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError, "embedding failure should return an error result")
}

// --- isToolNamePattern unit tests ---

func TestIsToolNamePattern(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"file_read", true},
		{"web_search", true},
		{"memory_before_turn", true},
		{"hello world", false},
		{"search the web", false},
		{"", false},
		{"nounderscores", false},
		{"has_one", true},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := isToolNamePattern(tt.query)
			require.Equal(t, tt.expected, got)
		})
	}
}
