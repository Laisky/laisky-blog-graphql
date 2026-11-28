package web

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestGraphQLFileRenameTargetProject pins the GraphQL peer of the MCP
// file_rename target_project argument to the same routing and refusals.
func TestGraphQLFileRenameTargetProject(t *testing.T) {
	const version = `expected_version:"0123456789abcdef0123456789abcdef:1"`

	t.Run("a target project reaches the backend in the rename request", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"q",`+version+`) { moved_count } }`)
		require.Empty(t, response.Errors)
		require.JSONEq(t, `{"moved_count":1}`, string(response.Data["FileRename"]))
		require.Equal(t, 1, plugin.renames)
		require.Equal(t, files.RenameRequest{Project: "p", FromPath: "/a", TargetProject: "q", ToPath: "/b"}, plugin.lastRename)
		require.Equal(t, "q", plugin.lastDestinationProject, "destination conditions are scoped to the target project")
	})

	t.Run("an empty target project renames within the project", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"",`+version+`) { moved_count } }`)
		require.Empty(t, response.Errors)
		require.Equal(t, 1, plugin.renames)
		require.Equal(t, "p", plugin.lastRename.Destination())
		require.Equal(t, "p", plugin.lastDestinationProject)
	})

	t.Run("an invalid target project is refused before storage", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"*",`+version+`) { moved_count } }`)
		require.Equal(t, string(files.ErrCodeInvalidPath), errorCode(t, response))
		require.Zero(t, plugin.attempts)
	})
}
