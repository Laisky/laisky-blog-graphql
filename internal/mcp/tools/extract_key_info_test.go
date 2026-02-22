package tools

import (
	"context"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

type stubKeyInfoService struct {
	input    rag.ExtractInput
	contexts []string
	err      error
}

func (s *stubKeyInfoService) ExtractKeyInfo(ctx context.Context, input rag.ExtractInput) ([]string, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	return s.contexts, nil
}

func TestExtractKeyInfoTool_HandleSuccess(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	settings := rag.Settings{TopKDefault: 2, TopKLimit: 5, MaxMaterialsSize: 1000, SemanticWeight: 0.5, LexicalWeight: 0.5}
	tool, err := NewExtractKeyInfoTool(
		svc,
		log.Logger.Named("extract_key_info_test"),
		func(ctx context.Context) string { return "Bearer task@sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		settings,
	)
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "q", "materials": "text"}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, svc.input.TaskID)
	require.Equal(t, "sk-test", svc.input.APIKey)
	require.Equal(t, 1, len(svc.contexts))
}

func TestExtractKeyInfoTool_HandleIgnoresProvidedTaskID(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	settings := rag.Settings{TopKDefault: 2, TopKLimit: 5, MaxMaterialsSize: 1000, SemanticWeight: 0.5, LexicalWeight: 0.5}
	tool, err := NewExtractKeyInfoTool(
		svc,
		log.Logger.Named("extract_key_info_test"),
		func(ctx context.Context) string { return "Bearer sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		settings,
	)
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "q", "materials": "text", "task_id": "workspace-1"}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, svc.input.TaskID)
	require.NotEqual(t, "workspace-1", svc.input.TaskID)
}

func TestExtractKeyInfoTool_InvalidTopK(t *testing.T) {
	svc := &stubKeyInfoService{}
	settings := rag.Settings{TopKDefault: 2, TopKLimit: 3, MaxMaterialsSize: 1000, SemanticWeight: 0.5, LexicalWeight: 0.5}
	tool, err := NewExtractKeyInfoTool(
		svc,
		log.Logger.Named("extract_key_info_test"),
		func(ctx context.Context) string { return "Bearer task@sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		settings,
	)
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"query":     "q",
		"materials": "text",
		"top_k":     10,
	}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestExtractKeyInfoTool_AllowsBlankProvidedTaskID(t *testing.T) {
	svc := &stubKeyInfoService{}
	settings := rag.Settings{TopKDefault: 2, TopKLimit: 5, MaxMaterialsSize: 1000, SemanticWeight: 0.5, LexicalWeight: 0.5}
	tool, err := NewExtractKeyInfoTool(
		svc,
		log.Logger.Named("extract_key_info_test"),
		func(ctx context.Context) string { return "Bearer sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		settings,
	)
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"query":     "q",
		"materials": "text",
		"task_id":   "   ",
	}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotEmpty(t, svc.input.TaskID)
}

func TestExtractKeyInfoTool_GeneratesUniqueTaskIDPerCall(t *testing.T) {
	svc := &stubKeyInfoService{contexts: []string{"ctx"}}
	settings := rag.Settings{TopKDefault: 2, TopKLimit: 5, MaxMaterialsSize: 1000, SemanticWeight: 0.5, LexicalWeight: 0.5}
	tool, err := NewExtractKeyInfoTool(
		svc,
		log.Logger.Named("extract_key_info_test"),
		func(ctx context.Context) string { return "Bearer sk-test" },
		func(context.Context, string, oneapi.Price, string) error { return nil },
		settings,
	)
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"query": "q", "materials": "text"}}}
	result, err := tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	first := svc.input.TaskID
	require.NotEmpty(t, first)

	result, err = tool.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	second := svc.input.TaskID
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second)
}
