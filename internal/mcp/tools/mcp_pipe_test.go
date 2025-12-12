package tools

import (
	"context"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestMCPPipeSequential verifies that mcp_pipe can run sequential steps and pass outputs.
func TestMCPPipeSequential(t *testing.T) {
	invoker := func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
		reqArgs, ok := args.(map[string]any)
		require.True(t, ok)
		switch toolName {
		case "web_search":
			require.Equal(t, "golang", reqArgs["query"])
			return mcp.NewToolResultJSON(map[string]any{
				"results": []any{
					map[string]any{"url": "https://example.com"},
				},
			})
		case "web_fetch":
			require.Equal(t, "https://example.com", reqArgs["url"])
			return mcp.NewToolResultJSON(map[string]any{"content": "hello world"})
		case "extract_key_info":
			require.Equal(t, "golang", reqArgs["query"])
			require.Equal(t, "hello world", reqArgs["materials"])
			return mcp.NewToolResultJSON(map[string]any{"contexts": []any{"c1", "c2"}})
		default:
			return mcp.NewToolResultError("unknown tool"), nil
		}
	}

	tool, err := NewMCPPipeTool(log.Logger.Named("test_mcp_pipe"), invoker, PipeLimits{MaxSteps: 10, MaxDepth: 2, MaxParallel: 4})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"steps": []any{
			map[string]any{"id": "s", "tool": "web_search", "args": map[string]any{"query": "${vars.q}"}},
			map[string]any{"id": "f", "tool": "web_fetch", "args": map[string]any{"url": map[string]any{"$ref": "steps.s.structured.results.0.url"}}},
			map[string]any{"id": "e", "tool": "extract_key_info", "args": map[string]any{"query": "${vars.q}", "materials": "${steps.f.structured.content}"}},
		},
		"vars":   map[string]any{"q": "golang"},
		"return": map[string]any{"$ref": "steps.e.structured.contexts"},
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	structured, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, structured["ok"])
	require.Equal(t, []any{"c1", "c2"}, structured["result"])
}

// TestMCPPipeParallel verifies that parallel groups run and can be referenced.
func TestMCPPipeParallel(t *testing.T) {
	invoker := func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
		reqArgs, ok := args.(map[string]any)
		require.True(t, ok)
		switch toolName {
		case "web_fetch":
			url := reqArgs["url"].(string)
			return mcp.NewToolResultJSON(map[string]any{"content": "content:" + url})
		case "extract_key_info":
			materials := reqArgs["materials"].(string)
			require.Contains(t, materials, "content:https://a")
			require.Contains(t, materials, "content:https://b")
			return mcp.NewToolResultJSON(map[string]any{"contexts": []any{"ok"}})
		default:
			return mcp.NewToolResultError("unknown tool"), nil
		}
	}

	tool, err := NewMCPPipeTool(log.Logger.Named("test_mcp_pipe_parallel"), invoker, PipeLimits{MaxSteps: 10, MaxDepth: 2, MaxParallel: 4})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"vars": map[string]any{"u1": "https://a", "u2": "https://b", "q": "q"},
		"steps": []any{
			map[string]any{"id": "pages", "parallel": []any{
				map[string]any{"id": "p1", "tool": "web_fetch", "args": map[string]any{"url": "${vars.u1}"}},
				map[string]any{"id": "p2", "tool": "web_fetch", "args": map[string]any{"url": "${vars.u2}"}},
			}},
			map[string]any{"id": "extract", "tool": "extract_key_info", "args": map[string]any{
				"query":     "${vars.q}",
				"materials": "${steps.pages.children.p1.structured.content}\n${steps.pages.children.p2.structured.content}",
			}},
		},
		"return": map[string]any{"$ref": "steps.extract.structured.contexts"},
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	structured := result.StructuredContent.(map[string]any)
	require.Equal(t, []any{"ok"}, structured["result"])
}

// TestMCPPipeNested verifies nested pipeline execution.
func TestMCPPipeNested(t *testing.T) {
	invoker := func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
		switch toolName {
		case "web_fetch":
			return mcp.NewToolResultJSON(map[string]any{"content": "nested"})
		default:
			return mcp.NewToolResultError("unknown tool"), nil
		}
	}

	tool, err := NewMCPPipeTool(log.Logger.Named("test_mcp_pipe_nested"), invoker, PipeLimits{MaxSteps: 10, MaxDepth: 3, MaxParallel: 4})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"steps": []any{
			map[string]any{"id": "child", "pipe": map[string]any{
				"steps": []any{
					map[string]any{"id": "f", "tool": "web_fetch", "args": map[string]any{"url": "https://x"}},
				},
				"return": map[string]any{"$ref": "steps.f.structured.content"},
			}},
		},
		"return": map[string]any{"$ref": "steps.child.result"},
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	structured := result.StructuredContent.(map[string]any)
	require.Equal(t, "nested", structured["result"])
}

// TestMCPPipeMaxSteps verifies MaxSteps enforcement.
func TestMCPPipeMaxSteps(t *testing.T) {
	invoker := func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultJSON(map[string]any{"ok": true})
	}

	tool, err := NewMCPPipeTool(log.Logger.Named("test_mcp_pipe_max_steps"), invoker, PipeLimits{MaxSteps: 2, MaxDepth: 2, MaxParallel: 4})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"steps": []any{
			map[string]any{"id": "a", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
			map[string]any{"id": "b", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
			map[string]any{"id": "c", "tool": "web_fetch", "args": map[string]any{"url": "x"}},
		},
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.True(t, result.IsError)

	structured := result.StructuredContent.(map[string]any)
	require.Equal(t, false, structured["ok"])
	require.NotEmpty(t, structured["error"])
}

// TestMCPPipeRefOverStructStructuredContent verifies that $ref paths can traverse
// structured tool outputs that are Go structs (they are normalized to JSON maps).
func TestMCPPipeRefOverStructStructuredContent(t *testing.T) {
	type searchItem struct {
		URL string `json:"url"`
	}
	type searchResult struct {
		Results []searchItem `json:"results"`
	}

	invoker := func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
		reqArgs, ok := args.(map[string]any)
		require.True(t, ok)
		switch toolName {
		case "web_search":
			return mcp.NewToolResultJSON(searchResult{Results: []searchItem{{URL: "https://modelcontextprotocol.io/"}}})
		case "web_fetch":
			require.Equal(t, "https://modelcontextprotocol.io/", reqArgs["url"])
			return mcp.NewToolResultJSON(map[string]any{"content": "ok"})
		default:
			return mcp.NewToolResultError("unknown tool"), nil
		}
	}

	tool, err := NewMCPPipeTool(log.Logger.Named("test_mcp_pipe_struct"), invoker, PipeLimits{MaxSteps: 10, MaxDepth: 2, MaxParallel: 4})
	require.NoError(t, err)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"vars": map[string]any{"q": "mcp protocol overview"},
		"steps": []any{
			map[string]any{"id": "search", "tool": "web_search", "args": map[string]any{"query": "${vars.q}"}},
			map[string]any{"id": "fetch", "tool": "web_fetch", "args": map[string]any{"url": map[string]any{"$ref": "steps.search.structured.results.0.url"}}},
		},
		"return": map[string]any{"$ref": "steps.fetch.structured.content"},
	}}}

	result, handleErr := tool.Handle(context.Background(), req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	structured := result.StructuredContent.(map[string]any)
	require.Equal(t, true, structured["ok"])
	require.Equal(t, "ok", structured["result"])
}
