package tools

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// These stubs exercise transport/error mapping with nominal valid requests.
// Real precondition enforcement is tested against RAG and PageIndex services.
func (stubFileService) SupportsFileVersionPreconditions() bool      { return true }
func (*behaviorFileService) SupportsFileVersionPreconditions() bool { return true }

// TestFileIORequiredToolSchemas checks the actual marshaled definitions clients
// receive, including the write alternative which ToolInputSchema cannot express.
func TestFileIORequiredToolSchemas(t *testing.T) {
	writer, err := NewFileWriteTool(stubFileService{})
	require.NoError(t, err)
	deleter, err := NewFileDeleteTool(stubFileService{})
	require.NoError(t, err)
	renamer, err := NewFileRenameTool(stubFileService{})
	require.NoError(t, err)
	reader, err := NewFileReadTool(stubFileService{})
	require.NoError(t, err)
	for _, tool := range []mcp.Tool{writer.Definition(), deleter.Definition(), renamer.Definition(), reader.Definition()} {
		encoded, err := json.Marshal(tool)
		require.NoError(t, err, "raw and structured schemas must never conflict")
		var definition struct {
			InputSchema struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
				OneOf      []struct {
					Required   []string                  `json:"required"`
					Properties map[string]map[string]any `json:"properties"`
					Not        map[string][]string       `json:"not"`
				} `json:"oneOf"`
			} `json:"inputSchema"`
		}
		require.NoError(t, json.Unmarshal(encoded, &definition))
		schema := definition.InputSchema
		require.Contains(t, schema.Properties, "expected_version")
		switch tool.Name {
		case "file_write":
			require.Len(t, schema.OneOf, 2)
			require.Contains(t, schema.OneOf[0].Required, "expected_version")
			require.Equal(t, false, schema.OneOf[0].Properties["create_only"]["const"])
			require.Contains(t, schema.OneOf[1].Required, "create_only")
			require.Equal(t, true, schema.OneOf[1].Properties["create_only"]["const"])
			require.Contains(t, schema.OneOf[1].Not["required"], "expected_version")
		case "file_delete", "file_rename":
			require.Contains(t, schema.Required, "expected_version")
		case "file_read":
			require.NotContains(t, schema.Required, "expected_version", "initial read must obtain its own token")
		}
	}
}

// TestFileIOClientRequiredConditions tests absent intent independently of the
// nominal request helpers used by unrelated transport and billing test fixtures.
func TestFileIOClientRequiredConditions(t *testing.T) {
	for _, operation := range []files.FileOperation{files.FileOperationWrite, files.FileOperationDelete, files.FileOperationRename, files.FileOperationRestore} {
		err := files.RequireClientFilePreconditions(operation, files.FilePreconditions{})
		require.True(t, files.IsCode(err, files.ErrCodePreconditionRequired))
	}
	require.NoError(t, files.RequireClientFilePreconditions(files.FileOperationRead, files.FilePreconditions{}))
	require.NoError(t, files.RequireClientFilePreconditions(files.FileOperationWrite, files.FilePreconditions{CreateOnly: true}))
}
