package web

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// crossProjectRecordingPlugin adds the cross-project rename capability to the
// recording backend and reports the destination scope the shared gate bound.
type crossProjectRecordingPlugin struct {
	*recordingPlugin
	moves              int
	targetProject      string
	destinationProject string
}

// RenameAcrossProjects records one cross-project move that passed its precondition.
func (p *crossProjectRecordingPlugin) RenameAcrossProjects(ctx context.Context, auth files.AuthContext, project, fromPath, targetProject, _ string, _ bool) (files.RenameResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe(ctx)
	p.attempts++
	if err := p.check(ctx, auth, project, fromPath, files.FileOperationRename); err != nil {
		return files.RenameResult{}, err
	}
	conditions, _ := files.ScopedFilePreconditions(ctx, auth, project, fromPath, files.FileOperationRename)
	p.moves++
	p.targetProject, p.destinationProject = targetProject, conditions.DestinationProject
	return files.RenameResult{MovedCount: 1}, nil
}

// TestGraphQLFileRenameTargetProject pins the GraphQL peer of the MCP
// file_rename target_project argument to the same routing and refusals.
func TestGraphQLFileRenameTargetProject(t *testing.T) {
	const version = `expected_version:"0123456789abcdef0123456789abcdef:1"`

	t.Run("a target project moves through the cross-project capability", func(t *testing.T) {
		plugin := &crossProjectRecordingPlugin{recordingPlugin: newRecordingPlugin()}
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"q",`+version+`) { moved_count } }`)
		require.Empty(t, response.Errors)
		require.JSONEq(t, `{"moved_count":1}`, string(response.Data["FileRename"]))
		require.Equal(t, 1, plugin.moves)
		require.Zero(t, plugin.renames)
		require.Equal(t, "q", plugin.targetProject)
		require.Equal(t, "q", plugin.destinationProject, "destination conditions are scoped to the target project")
	})

	t.Run("an empty target project renames within the project", func(t *testing.T) {
		plugin := &crossProjectRecordingPlugin{recordingPlugin: newRecordingPlugin()}
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"",`+version+`) { moved_count } }`)
		require.Empty(t, response.Errors)
		require.Equal(t, 1, plugin.renames)
		require.Zero(t, plugin.moves)
	})

	t.Run("an invalid target project is refused before storage", func(t *testing.T) {
		plugin := &crossProjectRecordingPlugin{recordingPlugin: newRecordingPlugin()}
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"*",`+version+`) { moved_count } }`)
		require.Equal(t, string(files.ErrCodeInvalidPath), errorCode(t, response))
		require.Zero(t, plugin.attempts)
	})

	t.Run("a backend without the capability refuses instead of renaming in place", func(t *testing.T) {
		plugin := newRecordingPlugin()
		resolver := newContractResolver(ResolverArgs{MCPFileService: plugin})
		response := execute(t, resolver, true,
			`mutation { FileRename(project:"p",from_path:"/a",to_path:"/b",target_project:"q",`+version+`) { moved_count } }`)
		require.Equal(t, string(files.ErrCodeInvalidArgument), errorCode(t, response))
		require.Zero(t, plugin.attempts)
		require.Zero(t, plugin.renames)
	})
}
