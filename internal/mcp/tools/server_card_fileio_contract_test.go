package tools

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	for _, tool := range []Tool{mustFileReadTool(t, svc), mustFileWriteTool(t, svc), mustFileDeleteTool(t, svc), mustFileRenameTool(t, svc), &FileHistoryTool{name: FileRestoreVersionToolName}} {
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
		if definition.Name == "file_write" || definition.Name == FileRestoreVersionToolName {
			// The card is a compact, hand-curated catalog: it may omit descriptions
			// and advertise stricter canonical values (content_encoding/mode enums,
			// integer offset) than runtime. Argument names, required sets, conditions
			// and the per-alternative argument sets must match runtime exactly.
			require.ElementsMatch(t, slices.Collect(maps.Keys(runtime["properties"].(map[string]any))), slices.Collect(maps.Keys(properties)), definition.Name)
			require.Equal(t, runtime["required"], static["required"], definition.Name)
			// The intentional differences are exactly these stricter keywords; any
			// other type/enum/pattern/const/default difference is drift.
			stricter := map[string][]string{"content_encoding": {"enum"}, "mode": {"enum"}, "offset": {"type", "minimum"}}
			for name, live := range runtime["properties"].(map[string]any) {
				for _, keyword := range []string{"type", "enum", "pattern", "const", "default"} {
					if slices.Contains(stricter[name], keyword) {
						continue
					}
					liveValue, cardValue := live.(map[string]any)[keyword], properties[name].(map[string]any)[keyword]
					if values, ok := liveValue.([]any); ok {
						require.ElementsMatch(t, values, cardValue, "%s.%s.%s", definition.Name, name, keyword)
						continue
					}
					require.Equal(t, liveValue, cardValue, "%s.%s.%s", definition.Name, name, keyword)
				}
			}
			require.Equal(t, runtime["properties"].(map[string]any)["expected_version"].(map[string]any)["pattern"], properties["expected_version"].(map[string]any)["pattern"])
			liveBranches := runtime["oneOf"].([]any)
			cardBranches := static["oneOf"].([]any)
			require.Len(t, cardBranches, len(liveBranches))
			for index, alternative := range cardBranches {
				branch := alternative.(map[string]any)
				live := liveBranches[index].(map[string]any)
				require.Equal(t, live["type"], branch["type"])
				require.Equal(t, live["required"], branch["required"])
				require.Equal(t, live["not"], branch["not"])
				fields := branch["properties"].(map[string]any)
				liveFields := live["properties"].(map[string]any)
				require.ElementsMatch(t, slices.Collect(maps.Keys(liveFields)), slices.Collect(maps.Keys(fields)),
					"%s alternative %d must declare the same arguments as runtime", definition.Name, index)
				for name, field := range fields {
					if name == createOnlyKey {
						require.Equal(t, liveFields[name].(map[string]any)["const"], field.(map[string]any)["const"])
						continue
					}
					// Retain the card's own (possibly stricter) root constraint in
					// each alternative instead of replacing it with the runtime one.
					require.Equal(t, properties[name], field)
				}
			}
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
