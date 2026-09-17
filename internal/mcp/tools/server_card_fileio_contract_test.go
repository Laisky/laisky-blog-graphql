package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStaticServerCardKeepsFileIOConditions binds representative static discovery
// to the runtime contract so newly added CAS requirements cannot disappear from
// the public card while tools/list remains correct.
func TestStaticServerCardKeepsFileIOConditions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "public", ".well-known", "mcp", "server-card.json"))
	require.NoError(t, err)
	var card struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(raw, &card))
	byName := make(map[string]map[string]any)
	for _, entry := range card.Tools {
		byName[entry.Name] = entry.InputSchema
	}
	svc := &behaviorFileService{}
	for _, tool := range []Tool{mustFileReadTool(t, svc), mustFileWriteTool(t, svc), mustFileDeleteTool(t, svc), mustFileRenameTool(t, svc)} {
		definition := tool.Definition()
		encoded, err := toolInputSchemaJSON(definition)
		require.NoError(t, err)
		var runtime map[string]any
		require.NoError(t, json.Unmarshal(encoded, &runtime))
		static := byName[definition.Name]
		require.NotNil(t, static, definition.Name)
		properties := static["properties"].(map[string]any)
		require.Contains(t, properties, "expected_version", definition.Name)
		require.Equal(t, "string", properties["expected_version"].(map[string]any)["type"])
		if definition.Name == "file_write" {
			require.Equal(t, runtime["oneOf"], static["oneOf"])
			require.Contains(t, properties, "create_only")
		}
		if definition.Name == "file_delete" || definition.Name == "file_rename" {
			require.Contains(t, static["required"], "expected_version")
		}
		if definition.Name == "file_rename" {
			require.Contains(t, properties, "expected_destination_version")
			require.Contains(t, properties, "destination_must_not_exist")
		}
	}
}
