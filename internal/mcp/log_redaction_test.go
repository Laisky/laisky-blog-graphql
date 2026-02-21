package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedactMCPBodyArguments verifies file_write content is redacted in MCP requests.
func TestRedactMCPBodyArguments(t *testing.T) {
	payload := map[string]any{
		"method": "call_tool",
		"params": map[string]any{
			"tool_name": "file_write",
			"arguments": map[string]any{
				"project": "proj",
				"path":    "/a.txt",
				"content": "secret-content",
			},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	redacted := redactMCPBody(string(data))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(redacted), &parsed))

	params := parsed["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	content := args["content"].(map[string]any)
	require.Equal(t, true, content["redacted"])
}

// TestRedactMCPBodyResponseContent verifies content fields are redacted in responses.
func TestRedactMCPBodyResponseContent(t *testing.T) {
	payload := map[string]any{
		"result": map[string]any{
			"content": "very-secret",
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	redacted := redactMCPBody(string(data))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(redacted), &parsed))

	result := parsed["result"].(map[string]any)
	content := result["content"].(map[string]any)
	require.Equal(t, true, content["redacted"])
}

// TestRedactMCPBodyMemoryArguments verifies memory payload arrays are redacted in MCP requests.
func TestRedactMCPBodyMemoryArguments(t *testing.T) {
	payload := map[string]any{
		"method": "call_tool",
		"params": map[string]any{
			"tool_name": "memory_after_turn",
			"arguments": map[string]any{
				"project":      "demo",
				"session_id":   "s-1",
				"turn_id":      "t-1",
				"input_items":  []any{map[string]any{"type": "message"}},
				"output_items": []any{map[string]any{"type": "message"}},
			},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	redacted := redactMCPBody(string(data))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(redacted), &parsed))

	params := parsed["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	inputItems := args["input_items"].(map[string]any)
	require.Equal(t, true, inputItems["redacted"])
	outputItems := args["output_items"].(map[string]any)
	require.Equal(t, true, outputItems["redacted"])
}
