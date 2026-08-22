package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/llm"
)

// OpenAIFileSummarizer generates file-level overviews via an OpenAI-compatible
// Responses API endpoint. It is distinct from OpenAIContextualizer, which situates
// individual chunks for retrieval (§4.4).
type OpenAIFileSummarizer struct {
	model  string
	helper *llm.ResponsesHelper
	cfg    fileSummaryGenConfig
}

// NewOpenAIFileSummarizer builds a summarizer from file-summary settings. It returns
// nil when no model is configured, so callers can treat a nil summarizer as "publish
// a deterministic fallback".
func NewOpenAIFileSummarizer(cfg FileSummarySettings, httpClient *http.Client) *OpenAIFileSummarizer {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil
	}
	helper := llm.NewResponsesHelper(cfg.BaseURL, cfg.Timeout, httpClient)
	if helper == nil {
		return nil
	}
	return &OpenAIFileSummarizer{
		model:  strings.TrimSpace(cfg.Model),
		helper: helper,
		cfg: fileSummaryGenConfig{
			targetWords:          cfg.TargetWords,
			maxWords:             cfg.MaxWords,
			maxInputTokens:       cfg.MaxInputTokens,
			maxReduceCalls:       cfg.MaxReduceCalls,
			maxTotalInputTokens:  cfg.MaxTotalInputTokens,
			maxTotalOutputTokens: cfg.MaxTotalOutputTokens,
		},
	}
}

// GenerateFileSummary implements FileSummarizer. It returns the raw model text; the
// caller normalizes and validates it and falls back deterministically on error.
func (s *OpenAIFileSummarizer) GenerateFileSummary(ctx context.Context, apiKey, wholeDocument string) (string, error) {
	if s == nil {
		return "", errors.New("file summarizer is nil")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", errors.New("missing api key for file summarization")
	}
	cacheKey := buildSummaryPromptCacheKey(wholeDocument)
	generate := func(ctx context.Context, instructions, input string, maxOutputTokens int) (string, error) {
		text, err := s.helper.CreateText(ctx, apiKey, llm.ResponseRequest{
			Model:           s.model,
			Instructions:    instructions,
			Input:           input,
			PromptCacheKey:  cacheKey,
			MaxOutputTokens: maxOutputTokens,
			Temperature:     0.1,
		})
		if err != nil {
			return "", errors.Wrap(err, "create file summary response")
		}
		return text, nil
	}
	return hierarchicalSummarize(ctx, s.cfg, wholeDocument, generate)
}

// buildSummaryPromptCacheKey returns a deterministic cache key for summary calls over
// the same document generation.
func buildSummaryPromptCacheKey(wholeDocument string) string {
	hash := sha256.Sum256([]byte(wholeDocument))
	return "mcp-files-file-summary-" + hex.EncodeToString(hash[:])
}
