package tools

import (
	"context"
	"encoding/json"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

type parityExtractor struct{ calls int }

func (p *parityExtractor) ExtractKeyInfo(context.Context, rag.ExtractInput) ([]string, error) {
	p.calls++
	return nil, nil
}

func TestExtractionRejectsNonIntegralTopKBeforeBilling(t *testing.T) {
	for _, value := range []any{1.5, "3", "3garbage", true, 0, 21, 1e100} {
		service := &parityExtractor{}
		charges := 0
		tool, err := NewExtractKeyInfoTool(service, log.Logger,
			func(context.Context) string { return "Bearer sk-contract-test-only" },
			func(context.Context, string, oneapi.Price, string) error { charges++; return nil },
			rag.Settings{TopKDefault: 5, TopKLimit: 20, MaxMaterialsSize: 2048})
		require.NoError(t, err)
		result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"query": "hello", "materials": "material", "top_k": value,
		}}})
		require.NoError(t, err)
		require.True(t, result.IsError, "%v", value)
		require.Zero(t, charges)
		require.Zero(t, service.calls)
	}
}

func TestExtractionEmptyResultIsAnArray(t *testing.T) {
	tool, err := NewExtractKeyInfoTool(&parityExtractor{}, log.Logger,
		func(context.Context) string { return "Bearer sk-contract-test-only" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		rag.Settings{TopKDefault: 5, TopKLimit: 20, MaxMaterialsSize: 2048})
	require.NoError(t, err)
	result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"query": "hello", "materials": "material",
	}}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.NotEmpty(t, envelope.Content)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &payload))
	require.Equal(t, []any{}, payload["contexts"])
}

func TestWebFetchDoesNotCoerceFormatArguments(t *testing.T) {
	charges, calls := 0, 0
	tool, err := NewWebFetchTool(&rlibs.DB{}, log.Logger,
		func(context.Context) string { return "sk-contract-test-only" },
		func(context.Context, string, oneapi.Price, string) error { charges++; return nil },
		func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error) { calls++; return nil, nil })
	require.NoError(t, err)
	for _, value := range []any{"false", 0, []bool{false}} {
		result, err := tool.Handle(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"url": "https://8.8.8.8/", "output_markdown": value,
		}}})
		require.NoError(t, err)
		require.True(t, result.IsError)
	}
	require.Zero(t, charges)
	require.Zero(t, calls)
}
