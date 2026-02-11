package files

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedactToolArguments ensures content is removed from file_write arguments.
func TestRedactToolArguments(t *testing.T) {
	args := map[string]any{"content": "secret"}
	redacted := RedactToolArguments("file_write", args)
	payload, ok := redacted["content"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, payload["redacted"])
}

// TestRedactToolResultChunks ensures chunk content is redacted in results.
func TestRedactToolResultChunks(t *testing.T) {
	result := map[string]any{
		"chunks": []any{
			map[string]any{
				"chunk_content": "secret",
			},
		},
	}
	redacted := RedactToolResult("file_search", result)
	chunks, ok := redacted["chunks"].([]any)
	require.True(t, ok)
	entry, ok := chunks[0].(map[string]any)
	require.True(t, ok)
	payload, ok := entry["chunk_content"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, payload["redacted"])
}
