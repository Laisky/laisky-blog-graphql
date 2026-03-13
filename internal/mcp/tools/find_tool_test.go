package tools

import (
	"context"
	"encoding/json"
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
}

func TestFindToolTool_SetToolsResetsIndex(t *testing.T) {
	tool := mustFindToolTool(t)

	// First query to build the index.
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "search"},
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
