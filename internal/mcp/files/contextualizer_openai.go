package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/llm"
)

// OpenAIContextualizer calls an OpenAI-compatible Responses API endpoint for chunk context generation.
type OpenAIContextualizer struct {
	model  string
	helper *llm.ResponsesHelper
}

// NewOpenAIContextualizer builds a contextualizer using the given endpoint and model.
func NewOpenAIContextualizer(baseURL, model string, timeout time.Duration, httpClient *http.Client) *OpenAIContextualizer {
	if strings.TrimSpace(model) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	helper := llm.NewResponsesHelper(baseURL, timeout, httpClient)
	if helper == nil {
		return nil
	}
	return &OpenAIContextualizer{
		model:  strings.TrimSpace(model),
		helper: helper,
	}
}

// GenerateChunkContexts generates one succinct context string per chunk.
func (c *OpenAIContextualizer) GenerateChunkContexts(ctx context.Context, apiKey, wholeDocument string, chunks []Chunk) ([]string, error) {
	if c == nil {
		return nil, errors.New("contextualizer is nil")
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("missing api key for chunk contextualization")
	}

	contexts := make([]string, 0, len(chunks))
	cacheKey := buildContextPromptCacheKey(wholeDocument)
	for _, chunk := range chunks {
		prompt := buildChunkContextPrompt(wholeDocument, chunk.Content)
		text, err := c.helper.CreateText(ctx, apiKey, llm.ResponseRequest{
			Model:           c.model,
			Instructions:    "Generate concise chunk context for retrieval. Return only the context text.",
			Input:           prompt,
			PromptCacheKey:  cacheKey,
			MaxOutputTokens: 128,
			Temperature:     0.1,
		})
		if err != nil {
			return nil, errors.Wrap(err, "create chunk context response")
		}
		contextText := strings.TrimSpace(text)
		if contextText == "" {
			return nil, errors.New("chunk context response returned empty content")
		}
		contexts = append(contexts, contextText)
	}

	return contexts, nil
}

// buildChunkContextPrompt creates the prompt that situates one chunk within the whole document.
func buildChunkContextPrompt(wholeDocument, chunkContent string) string {
	return "<document>\n" + wholeDocument +
		"\n</document>\nHere is the chunk we want to situate within the whole document\n<chunk>\n" + chunkContent +
		"\n</chunk>\nPlease give a short succinct context to situate this chunk within the overall document for the purposes of improving search retrieval of the chunk. Answer only with the succinct context and nothing else."
}

// buildContextPromptCacheKey returns a deterministic cache key for chunk contextualization calls over the same document.
func buildContextPromptCacheKey(wholeDocument string) string {
	hash := sha256.Sum256([]byte(wholeDocument))
	return "mcp-files-contextualize-" + hex.EncodeToString(hash[:])
}
