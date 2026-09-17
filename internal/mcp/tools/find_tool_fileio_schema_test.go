package tools

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestFindToolFileIOSchemaMatchesToolsList compares the actual serialized public
// schemas, not the library's structured-only InputSchema field.
func TestFindToolFileIOSchemaMatchesToolsList(t *testing.T) {
	svc := &behaviorFileService{}
	all := []Tool{
		mustFileStatTool(t, svc), mustFileReadTool(t, svc), mustFileWriteTool(t, svc),
		mustFileDeleteTool(t, svc), mustFileRenameTool(t, svc),
		mustFileListTool(t, svc), mustFileSearchTool(t, svc),
	}
	for _, candidate := range all {
		t.Run(candidate.Definition().Name, func(t *testing.T) {
			definition := candidate.Definition()
			finder := &FindToolTool{logger: log.Logger}
			finder.SetTools([]mcp.Tool{definition})
			result, err := finder.buildResponse([]string{definition.Name}, false)
			require.NoError(t, err)
			require.False(t, result.IsError)
			payload := behaviorJSONContent(t, result)
			found := payload["tools"].([]any)[0].(map[string]any)
			actual, err := json.Marshal(found["inputSchema"])
			require.NoError(t, err)
			direct, err := json.Marshal(definition)
			require.NoError(t, err)
			var listed struct {
				InputSchema json.RawMessage `json:"inputSchema"`
			}
			require.NoError(t, json.Unmarshal(direct, &listed))
			require.JSONEq(t, string(listed.InputSchema), string(actual))
			if definition.Name == "file_write" {
				require.Contains(t, string(actual), `"oneOf"`)
				require.Contains(t, string(actual), `"expected_version"`)
				require.Contains(t, string(actual), `"create_only"`)
			}
		})
	}
}

func TestFindToolIndexesRawSchemaParameters(t *testing.T) {
	definition := mcp.NewToolWithRawSchema("fixture", "no parameter words in this description",
		json.RawMessage(`{"type":"object","properties":{"unique_cas_parameter":{"type":"string"}},"required":["unique_cas_parameter"]}`))
	finder := &FindToolTool{logger: log.Logger}
	finder.SetTools([]mcp.Tool{definition})
	names, err := finder.searchRegex("unique_cas_parameter")
	require.NoError(t, err)
	require.Equal(t, []string{"fixture"}, names)
	require.Equal(t, []string{"fixture"}, finder.searchBM25("unique_cas_parameter"))
}

func TestToolInputSchemaRejectsMalformedOrConflictingRepresentations(t *testing.T) {
	definition := mcp.NewToolWithRawSchema("broken", "", json.RawMessage(`{`))
	_, err := toolInputSchemaJSON(definition)
	require.Error(t, err)
	definition = mcp.NewTool("conflicting")
	definition.RawInputSchema = json.RawMessage(`{"type":"object"}`)
	_, err = toolInputSchemaJSON(definition)
	require.Error(t, err)
}

func TestFindToolConcurrentDefinitionReplacement(t *testing.T) {
	finder := &FindToolTool{logger: log.Logger}
	definition := mcp.NewToolWithRawSchema("fixture", "", json.RawMessage(`{"type":"object","properties":{}}`))
	finder.SetTools([]mcp.Tool{definition})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for range 100 {
			finder.SetTools(nil)
			finder.SetTools([]mcp.Tool{definition})
		}
	}()
	for range 100 {
		result, err := finder.buildResponse([]string{"fixture"}, false)
		require.NoError(t, err)
		require.False(t, result.IsError)
	}
	group.Wait()
}
