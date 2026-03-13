package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	pgvector "github.com/pgvector/pgvector-go"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

const findToolDefaultTopK = 5

// toolDoc holds the pre-computed text, tokens, and embedding for one tool definition.
type toolDoc struct {
	Name      string
	Text      string
	Tokens    []string
	Embedding pgvector.Vector
}

// FindToolTool exposes the find_tool MCP capability for progressive tool discovery.
type FindToolTool struct {
	embedder       rag.Embedder
	settings       rag.Settings
	logger         logSDK.Logger
	headerProvider AuthorizationHeaderProvider
	billingChecker BillingChecker

	mu       sync.Mutex
	tools    []mcp.Tool // registered tool definitions (set once via SetTools)
	toolDocs []toolDoc  // lazily built on first query
}

// NewFindToolTool wires the dependencies for the find_tool handler.
func NewFindToolTool(
	embedder rag.Embedder,
	logger logSDK.Logger,
	headerProvider AuthorizationHeaderProvider,
	billingChecker BillingChecker,
	settings rag.Settings,
) (*FindToolTool, error) {
	if embedder == nil {
		return nil, errors.New("embedder is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if headerProvider == nil {
		return nil, errors.New("authorization header provider is required")
	}
	if billingChecker == nil {
		return nil, errors.New("billing checker is required")
	}
	if settings.SemanticWeight == 0 {
		settings = rag.LoadSettingsFromConfig()
	}

	return &FindToolTool{
		embedder:       embedder,
		logger:         logger,
		headerProvider: headerProvider,
		billingChecker: billingChecker,
		settings:       settings,
	}, nil
}

// SetTools provides the full list of registered tool definitions to search over.
// Must be called before the first Handle invocation.
func (t *FindToolTool) SetTools(tools []mcp.Tool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tools = tools
	t.toolDocs = nil // reset cache so next query re-indexes
}

// Definition returns the MCP metadata for the find_tool contract.
func (t *FindToolTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"find_tool",
		mcp.WithDescription(
			"Search for MCP tools by natural-language query. "+
				"Returns the top matching tool schemas (name, description, parameters). "+
				"Use this to discover available tools when you don't know the exact tool name.",
		),
		mcp.WithString(
			"query",
			mcp.Required(),
			mcp.Description("Natural-language description of the capability you are looking for."),
		),
	)
}

// Handle executes the find_tool workflow: embed query, hybrid-rank tools, return top matches.
func (t *FindToolTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return mcp.NewToolResultError("query cannot be empty"), nil
	}

	header := t.headerProvider(ctx)
	authCtx, err := mcpauth.FromContextOrHeader(ctx, header)
	if err != nil {
		t.logger.Warn("find_tool authorization failed", zap.Error(err))
		return mcp.NewToolResultError(err.Error()), nil
	}

	if err := t.billingChecker(ctx, authCtx.APIKey, oneapi.PriceFindTool, "find_tool"); err != nil {
		t.logger.Warn("find_tool billing denied", zap.Error(err))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	// Ensure tool docs are indexed (lazy, one-time).
	if err := t.ensureIndex(ctx, authCtx.APIKey); err != nil {
		t.logger.Error("find_tool index failed", zap.Error(err))
		return mcp.NewToolResultError("failed to initialize tool index"), nil
	}

	// Embed the query.
	queryVecs, err := t.embedder.EmbedTexts(ctx, authCtx.APIKey, []string{query})
	if err != nil {
		t.logger.Error("find_tool embed query failed", zap.Error(err))
		return mcp.NewToolResultError("failed to embed query"), nil
	}
	if len(queryVecs) == 0 {
		return mcp.NewToolResultError("embedding provider returned no query vector"), nil
	}
	queryVec := queryVecs[0]

	// Hybrid rank.
	queryTokens := rag.Tokenize(query)
	tokenSet := make(map[string]struct{}, len(queryTokens))
	for _, tok := range queryTokens {
		tokenSet[tok] = struct{}{}
	}

	type scored struct {
		name  string
		score float64
	}

	t.mu.Lock()
	docs := t.toolDocs
	t.mu.Unlock()

	scoredTools := make([]scored, 0, len(docs))
	for _, doc := range docs {
		semantic := rag.CosineSimilarity(queryVec, doc.Embedding)
		lexical := rag.LexicalScore(doc.Tokens, tokenSet)
		score := t.settings.SemanticWeight*semantic + t.settings.LexicalWeight*lexical
		scoredTools = append(scoredTools, scored{name: doc.Name, score: score})
	}

	sort.Slice(scoredTools, func(i, j int) bool {
		return scoredTools[i].score > scoredTools[j].score
	})

	// Collect top-K tool schemas.
	topK := findToolDefaultTopK
	if topK > len(scoredTools) {
		topK = len(scoredTools)
	}

	// Build name→definition map for quick lookup.
	toolMap := make(map[string]mcp.Tool, len(t.tools))
	t.mu.Lock()
	for _, tool := range t.tools {
		toolMap[tool.Name] = tool
	}
	t.mu.Unlock()

	results := make([]map[string]any, 0, topK)
	for i := 0; i < topK; i++ {
		name := scoredTools[i].name
		def, ok := toolMap[name]
		if !ok {
			continue
		}
		results = append(results, map[string]any{
			"name":        def.Name,
			"description": def.Description,
			"inputSchema": def.InputSchema,
		})
	}

	payload := map[string]any{
		"tools": results,
	}
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		t.logger.Error("find_tool encode response", zap.Error(err))
		return mcp.NewToolResultError("failed to encode find_tool response"), nil
	}
	return result, nil
}

// ensureIndex lazily builds the in-memory index (text + tokens + embeddings) for all tools.
func (t *FindToolTool) ensureIndex(ctx context.Context, apiKey string) error {
	t.mu.Lock()
	if t.toolDocs != nil {
		t.mu.Unlock()
		return nil
	}
	tools := t.tools
	t.mu.Unlock()

	if len(tools) == 0 {
		return errors.New("no tools registered")
	}

	// Build text documents for each tool.
	texts := make([]string, 0, len(tools))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		doc := buildToolDocument(tool)
		texts = append(texts, doc)
		names = append(names, tool.Name)
	}

	// Tokenize all documents.
	allTokens := make([][]string, len(texts))
	for i, text := range texts {
		allTokens[i] = rag.Tokenize(text)
	}

	// Embed all documents.
	embeddings, err := t.embedder.EmbedTexts(ctx, apiKey, texts)
	if err != nil {
		return errors.Wrap(err, "embed tool documents")
	}
	if len(embeddings) != len(texts) {
		return errors.New("embedding count mismatch")
	}

	// Build docs.
	docs := make([]toolDoc, len(tools))
	for i := range tools {
		docs[i] = toolDoc{
			Name:      names[i],
			Text:      texts[i],
			Tokens:    allTokens[i],
			Embedding: embeddings[i],
		}
	}

	t.mu.Lock()
	t.toolDocs = docs
	t.mu.Unlock()

	t.logger.Info("find_tool index built", zap.Int("tools", len(docs)))
	return nil
}

// buildToolDocument creates a searchable text representation of a tool definition.
func buildToolDocument(tool mcp.Tool) string {
	var sb strings.Builder
	sb.WriteString("Tool: ")
	sb.WriteString(tool.Name)
	sb.WriteString("\n")
	if tool.Description != "" {
		sb.WriteString("Description: ")
		sb.WriteString(tool.Description)
		sb.WriteString("\n")
	}

	if len(tool.InputSchema.Properties) > 0 {
		sb.WriteString("Parameters:\n")
		schemaBytes, err := json.Marshal(tool.InputSchema)
		if err == nil {
			sb.WriteString(string(schemaBytes))
		}
	}

	return sb.String()
}
