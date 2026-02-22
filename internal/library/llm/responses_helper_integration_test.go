package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResponsesHelperCreateTextWithRealAPI optionally verifies real external Responses API connectivity.
func TestResponsesHelperCreateTextWithRealAPI(t *testing.T) {
	t.Parallel()

	apiKey := strings.TrimSpace(os.Getenv("LLM_API_KEY"))
	apiBase := strings.TrimSpace(os.Getenv("LLM_API_BASE"))
	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	if model == "" {
		model = "openai/gpt-oss-120b"
	}

	if apiKey == "" {
		t.Skip("skip real API test: LLM_API_KEY is not set")
	}

	helper := NewResponsesHelper(apiBase, 20*time.Second, nil)
	text, err := helper.CreateText(context.Background(), apiKey, ResponseRequest{
		Model:           model,
		Instructions:    "Return only one short sentence.",
		Input:           "Say hello in one short sentence.",
		MaxOutputTokens: 64,
		Temperature:     0.1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(text))
}
