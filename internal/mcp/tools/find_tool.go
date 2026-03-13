package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
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

// Search mode constants.
const (
	// FindToolModeAuto selects the best search strategy automatically.
	// Uses regex when the query looks like a regex pattern, otherwise uses BM25.
	FindToolModeAuto = "auto"
	// FindToolModeRegex matches tools using a regex pattern against name and description.
	FindToolModeRegex = "regex"
	// FindToolModeBM25 ranks tools using BM25 term-frequency scoring.
	FindToolModeBM25 = "bm25"
	// FindToolModeEmbedding ranks tools using hybrid semantic + lexical scoring (requires embedding API).
	FindToolModeEmbedding = "embedding"
)

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
	toolDocs []toolDoc  // lazily built on first embedding query
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
			"Search for available MCP tools by query. Supports multiple search modes: "+
				"regex (pattern matching on tool names/descriptions), "+
				"bm25 (keyword relevance ranking), "+
				"embedding (semantic similarity via embeddings), "+
				"or auto (automatically selects the best mode). "+
				"Returns matching tool schemas or tool_reference names for deferred loading. "+
				"Use this to discover available tools when you don't know the exact tool name.",
		),
		mcp.WithString(
			"query",
			mcp.Required(),
			mcp.Description(
				"Search query. For regex mode: a Python-style regex pattern (e.g. 'file_.*', '(?i)web'). "+
					"For bm25/embedding/auto modes: a natural-language description of the capability you need.",
			),
		),
		mcp.WithString(
			"mode",
			mcp.Description(
				"Search mode: 'auto' (default, selects regex or bm25 based on query pattern), "+
					"'regex' (pattern match), 'bm25' (keyword ranking), "+
					"'embedding' (semantic + lexical hybrid, requires embedding API call).",
			),
		),
		mcp.WithBoolean(
			"return_references_only",
			mcp.Description(
				"When true, return only tool names (tool_reference format) instead of full schemas. "+
					"Use this for Anthropic API tool_reference compatibility. Default: false.",
			),
		),
	)
}

// Handle executes the find_tool workflow with the selected search mode.
func (t *FindToolTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return mcp.NewToolResultError("query cannot be empty"), nil
	}

	// Parse optional parameters.
	mode := FindToolModeAuto
	if modeStr, ok := optionalString(req, "mode"); ok && modeStr != "" {
		modeStr = strings.ToLower(strings.TrimSpace(modeStr))
		switch modeStr {
		case FindToolModeRegex, FindToolModeBM25, FindToolModeEmbedding, FindToolModeAuto:
			mode = modeStr
		default:
			return mcp.NewToolResultError(fmt.Sprintf("unsupported mode %q; use auto, regex, bm25, or embedding", modeStr)), nil
		}
	}

	refsOnly := optionalBool(req, "return_references_only")

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

	// Auto mode: detect regex patterns.
	if mode == FindToolModeAuto {
		mode = detectSearchMode(query)
	}

	t.logger.Debug("find_tool search",
		zap.String("query", query),
		zap.String("mode", mode),
		zap.Bool("refs_only", refsOnly),
	)

	var rankedNames []string
	switch mode {
	case FindToolModeRegex:
		rankedNames, err = t.searchRegex(query)
	case FindToolModeBM25:
		rankedNames = t.searchBM25(query)
	case FindToolModeEmbedding:
		rankedNames, err = t.searchEmbedding(ctx, authCtx.APIKey, query)
	default:
		rankedNames = t.searchBM25(query)
	}
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Clamp to top-K.
	topK := findToolDefaultTopK
	if topK > len(rankedNames) {
		topK = len(rankedNames)
	}
	rankedNames = rankedNames[:topK]

	return t.buildResponse(rankedNames, refsOnly)
}

// searchRegex matches tools using a compiled regex pattern against their searchable text.
func (t *FindToolTool) searchRegex(pattern string) ([]string, error) {
	if len(pattern) > 200 {
		return nil, errors.New("regex pattern exceeds 200 character limit")
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, errors.Errorf("invalid regex pattern: %v", err)
	}

	t.mu.Lock()
	tools := t.tools
	t.mu.Unlock()

	var matched []string
	for _, tool := range tools {
		doc := buildToolDocument(tool)
		if re.MatchString(doc) {
			matched = append(matched, tool.Name)
		}
	}

	return matched, nil
}

// searchBM25 ranks tools using BM25 term-frequency scoring (no embedding API needed).
func (t *FindToolTool) searchBM25(query string) []string {
	queryTokens := rag.Tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	t.mu.Lock()
	tools := t.tools
	t.mu.Unlock()

	// Build per-tool token lists and compute corpus-level stats.
	type docInfo struct {
		name   string
		tokens []string
	}
	docs := make([]docInfo, 0, len(tools))
	var totalTokens int
	for _, tool := range tools {
		text := buildToolDocument(tool)
		toks := rag.Tokenize(text)
		docs = append(docs, docInfo{name: tool.Name, tokens: toks})
		totalTokens += len(toks)
	}

	if len(docs) == 0 {
		return nil
	}

	avgDL := float64(totalTokens) / float64(len(docs))

	// Count document frequency for each query term.
	df := make(map[string]int, len(queryTokens))
	for _, qTok := range queryTokens {
		for _, doc := range docs {
			for _, dTok := range doc.tokens {
				if dTok == qTok {
					df[qTok]++
					break
				}
			}
		}
	}

	// BM25 scoring constants.
	const k1 = 1.2
	const b = 0.75
	n := float64(len(docs))

	type scored struct {
		name  string
		score float64
	}

	scoredDocs := make([]scored, 0, len(docs))
	for _, doc := range docs {
		// Count term frequencies in this document.
		tf := make(map[string]int, len(queryTokens))
		for _, dTok := range doc.tokens {
			for _, qTok := range queryTokens {
				if dTok == qTok {
					tf[qTok]++
				}
			}
		}

		var score float64
		dl := float64(len(doc.tokens))
		for _, qTok := range queryTokens {
			tfVal := float64(tf[qTok])
			dfVal := float64(df[qTok])
			if dfVal == 0 {
				continue
			}

			// IDF: log((N - df + 0.5) / (df + 0.5) + 1)
			idf := math.Log((n-dfVal+0.5)/(dfVal+0.5) + 1)
			// TF normalization: (tf * (k1 + 1)) / (tf + k1 * (1 - b + b * dl / avgdl))
			tfNorm := (tfVal * (k1 + 1)) / (tfVal + k1*(1-b+b*dl/avgDL))
			score += idf * tfNorm
		}

		scoredDocs = append(scoredDocs, scored{name: doc.name, score: score})
	}

	sort.Slice(scoredDocs, func(i, j int) bool {
		return scoredDocs[i].score > scoredDocs[j].score
	})

	// Filter out zero-score results.
	result := make([]string, 0, len(scoredDocs))
	for _, s := range scoredDocs {
		if s.score <= 0 {
			break
		}
		result = append(result, s.name)
	}

	return result
}

// searchEmbedding uses the original hybrid semantic + lexical scoring approach.
func (t *FindToolTool) searchEmbedding(ctx context.Context, apiKey, query string) ([]string, error) {
	// Ensure tool docs are indexed (lazy, one-time).
	if err := t.ensureIndex(ctx, apiKey); err != nil {
		t.logger.Error("find_tool index failed", zap.Error(err))
		return nil, errors.New("failed to initialize tool index")
	}

	// Embed the query.
	queryVecs, err := t.embedder.EmbedTexts(ctx, apiKey, []string{query})
	if err != nil {
		t.logger.Error("find_tool embed query failed", zap.Error(err))
		return nil, errors.New("failed to embed query")
	}
	if len(queryVecs) == 0 {
		return nil, errors.New("embedding provider returned no query vector")
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

	result := make([]string, 0, len(scoredTools))
	for _, s := range scoredTools {
		result = append(result, s.name)
	}

	return result, nil
}

// buildResponse constructs the MCP tool result from ranked tool names.
func (t *FindToolTool) buildResponse(rankedNames []string, refsOnly bool) (*mcp.CallToolResult, error) {
	// Build name→definition map for quick lookup.
	toolMap := make(map[string]mcp.Tool, len(t.tools))
	t.mu.Lock()
	for _, tool := range t.tools {
		toolMap[tool.Name] = tool
	}
	t.mu.Unlock()

	if refsOnly {
		// Return tool_reference format compatible with Anthropic API.
		refs := make([]map[string]any, 0, len(rankedNames))
		for _, name := range rankedNames {
			if _, ok := toolMap[name]; !ok {
				continue
			}
			refs = append(refs, map[string]any{
				"type":      "tool_reference",
				"tool_name": name,
			})
		}
		payload := map[string]any{
			"tool_references": refs,
		}
		result, err := mcp.NewToolResultJSON(payload)
		if err != nil {
			t.logger.Error("find_tool encode response", zap.Error(err))
			return mcp.NewToolResultError("failed to encode find_tool response"), nil
		}
		return result, nil
	}

	// Return full tool schemas (original format).
	results := make([]map[string]any, 0, len(rankedNames))
	for _, name := range rankedNames {
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

// detectSearchMode examines a query string to determine the appropriate search mode.
// Returns regex mode if the query contains regex metacharacters, otherwise bm25.
func detectSearchMode(query string) string {
	// Common regex metacharacters that wouldn't appear in natural language.
	regexIndicators := []string{".*", ".+", "\\w", "\\d", "\\s", "\\b", "[", "]", "(?", "|", "^", "$", "{", "}"}
	for _, indicator := range regexIndicators {
		if strings.Contains(query, indicator) {
			return FindToolModeRegex
		}
	}
	// If query contains only underscores and alphanumeric chars (looks like a tool name pattern).
	if isToolNamePattern(query) {
		return FindToolModeRegex
	}
	return FindToolModeBM25
}

// isToolNamePattern checks if the query looks like a tool name pattern (e.g. "file_", "web_search").
func isToolNamePattern(query string) bool {
	if len(query) == 0 {
		return false
	}
	hasUnderscore := false
	for _, ch := range query {
		if ch == '_' {
			hasUnderscore = true
			continue
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			continue
		}
		// Contains spaces or other chars — likely natural language.
		return false
	}
	return hasUnderscore
}

// optionalString extracts an optional string parameter from a tool request.
func optionalString(req mcp.CallToolRequest, key string) (string, bool) {
	args := req.GetArguments()
	if args == nil {
		return "", false
	}
	v, ok := args[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// optionalBool extracts an optional boolean parameter from a tool request.
func optionalBool(req mcp.CallToolRequest, key string) bool {
	return req.GetBool(key, false)
}
