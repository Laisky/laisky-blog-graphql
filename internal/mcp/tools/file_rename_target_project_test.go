package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// testRenameVersion is a syntactically valid opaque file version.
const testRenameVersion = "00000000000000000000000000000001:1"

// crossProjectFileService records cross-project moves and the destination
// scope of the conditions that reached it.
type crossProjectFileService struct {
	*behaviorFileService
	moves              int
	targetProject      string
	destinationProject string
}

// RenameAcrossProjects records one cross-project move request.
func (s *crossProjectFileService) RenameAcrossProjects(ctx context.Context, auth files.AuthContext, project, fromPath, targetProject, toPath string, _ bool) (files.RenameResult, error) {
	s.moves++
	s.lastProject, s.lastPath, s.lastToPath, s.targetProject = project, fromPath, toPath, targetProject
	conditions, ok := files.ScopedFilePreconditions(ctx, auth, project, fromPath, files.FileOperationRename)
	if !ok {
		return files.RenameResult{}, files.NewError(files.ErrCodePreconditionRequired, "no precondition reached the backend", false)
	}
	s.destinationProject = conditions.DestinationProject
	return files.RenameResult{MovedCount: 1}, nil
}

// renameRequest builds a file_rename call with the given extra arguments.
func renameRequest(extra map[string]any) mcp.CallToolRequest {
	args := map[string]any{"project": "proj", "from_path": "/a.txt", "to_path": "/b.txt", "expected_version": testRenameVersion}
	for key, value := range extra {
		args[key] = value
	}
	return newToolReq(args)
}

// TestFileRenameDefinitionAdvertisesOptionalTargetProject verifies the schema
// exposes target_project as an optional string.
func TestFileRenameDefinitionAdvertisesOptionalTargetProject(t *testing.T) {
	tool, err := NewFileRenameTool(&behaviorFileService{})
	require.NoError(t, err)
	encoded, err := toolInputSchemaJSON(tool.Definition())
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(encoded, &schema))
	property, ok := schema["properties"].(map[string]any)[targetProjectKey].(map[string]any)
	require.True(t, ok, "target_project must be declared")
	require.Equal(t, "string", property["type"])
	require.NotContains(t, schema["required"], targetProjectKey)
}

// TestFileRenameTargetProjectRouting verifies that only a real target project
// reaches the cross-project capability, carrying its destination scope.
func TestFileRenameTargetProjectRouting(t *testing.T) {
	ctx := authtest.WithVerified(context.Background(), "key")

	for name, extra := range map[string]map[string]any{
		"absent":           nil,
		"null":             {targetProjectKey: nil},
		"empty":            {targetProjectKey: ""},
		"same as project":  {targetProjectKey: "proj"},
		"plugin selection": {"plugin": "auto"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &crossProjectFileService{behaviorFileService: &behaviorFileService{renameResult: files.RenameResult{MovedCount: 1}}}
			tool, err := NewFileRenameTool(svc)
			require.NoError(t, err)
			result, err := tool.Handle(ctx, renameRequest(extra))
			require.NoError(t, err)
			require.False(t, result.IsError, behaviorTextContent(t, result))
			require.Zero(t, svc.moves, "a same-project rename must use Rename")
			require.Equal(t, "proj", svc.lastProject)
			require.Equal(t, "/b.txt", svc.lastToPath)
		})
	}

	t.Run("another project moves across projects", func(t *testing.T) {
		svc := &crossProjectFileService{behaviorFileService: &behaviorFileService{}}
		tool, err := NewFileRenameTool(svc)
		require.NoError(t, err)
		result, err := tool.Handle(ctx, renameRequest(map[string]any{targetProjectKey: "archive", "to_path": "inbox/b.txt"}))
		require.NoError(t, err)
		require.False(t, result.IsError, behaviorTextContent(t, result))
		require.Equal(t, 1, svc.moves)
		require.Equal(t, "proj", svc.lastProject)
		require.Equal(t, "archive", svc.targetProject)
		require.Equal(t, "/inbox/b.txt", svc.lastToPath, "the destination path is normalized as usual")
		require.Equal(t, "archive", svc.destinationProject, "destination conditions are scoped to the target project")
		require.Equal(t, 1, asInt(t, decodeToolPayload(t, result)["moved_count"]))
	})

	t.Run("a non-string target is refused before the backend", func(t *testing.T) {
		svc := &crossProjectFileService{behaviorFileService: &behaviorFileService{}}
		tool, err := NewFileRenameTool(svc)
		require.NoError(t, err)
		result, err := tool.Handle(ctx, renameRequest(map[string]any{targetProjectKey: 7}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Equal(t, string(files.ErrCodeInvalidArgument), behaviorJSONContent(t, result)["code"])
		require.Zero(t, svc.moves)
		require.Empty(t, svc.lastToPath, "the move must not fall back to a same-project rename")
	})

	for _, invalid := range []string{"*", "bad project", "bad/project"} {
		t.Run("invalid target "+invalid, func(t *testing.T) {
			svc := &crossProjectFileService{behaviorFileService: &behaviorFileService{}}
			tool, err := NewFileRenameTool(svc)
			require.NoError(t, err)
			result, err := tool.Handle(ctx, renameRequest(map[string]any{targetProjectKey: invalid}))
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, string(files.ErrCodeInvalidPath), behaviorJSONContent(t, result)["code"])
			require.Zero(t, svc.moves)
			require.Empty(t, svc.lastToPath)
		})
	}

	t.Run("a backend without the capability refuses instead of renaming in place", func(t *testing.T) {
		svc := &behaviorFileService{renameResult: files.RenameResult{MovedCount: 1}}
		tool, err := NewFileRenameTool(svc)
		require.NoError(t, err)
		result, err := tool.Handle(ctx, renameRequest(map[string]any{targetProjectKey: "archive"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		payload := behaviorJSONContent(t, result)
		require.Equal(t, string(files.ErrCodeInvalidArgument), payload["code"])
		require.Contains(t, payload["message"], "target_project")
		require.Empty(t, svc.lastToPath, "the backend's same-project Rename must not run")
	})

	t.Run("a move without a source version is refused", func(t *testing.T) {
		svc := &crossProjectFileService{behaviorFileService: &behaviorFileService{}}
		tool, err := NewFileRenameTool(svc)
		require.NoError(t, err)
		result, err := tool.Handle(ctx, newToolReq(map[string]any{"project": "proj", "from_path": "/a.txt", "to_path": "/b.txt", targetProjectKey: "archive"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Equal(t, string(files.ErrCodePreconditionRequired), behaviorJSONContent(t, result)["code"])
		require.Zero(t, svc.moves)
	})
}

// TestFileRenameTargetProjectEndToEnd drives file_rename with target_project
// through the production plugin manager, the rag plugin and a real service.
func TestFileRenameTargetProjectEndToEnd(t *testing.T) {
	svc := newE2EFileService(t, false)
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, mustE2EPlugin(t, svc))
	require.NoError(t, err)
	authCtx := authtest.WithVerified(context.Background(), "key")
	writeTool, err := NewFileWriteTool(manager)
	require.NoError(t, err)
	readTool, err := NewFileReadTool(manager)
	require.NoError(t, err)
	renameTool, err := NewFileRenameTool(manager)
	require.NoError(t, err)

	write := func(project, path, content string) string {
		t.Helper()
		result, err := writeTool.Handle(authCtx, newToolReq(map[string]any{
			"project": project, "path": path, "content": content, "mode": "TRUNCATE", "create_only": true,
		}))
		require.NoError(t, err)
		require.False(t, result.IsError, behaviorTextContent(t, result))
		return decodeToolPayload(t, result)["version"].(string)
	}
	read := func(project, path string) *mcp.CallToolResult {
		t.Helper()
		result, err := readTool.Handle(authCtx, newToolReq(map[string]any{"project": project, "path": path}))
		require.NoError(t, err)
		return result
	}

	source := write("proj", "/notes/a.txt", "moving bytes")
	occupied := write("archive", "/taken.txt", "already here")

	conflict, err := renameTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj", "from_path": "/notes/a.txt", "to_path": "/taken.txt", targetProjectKey: "archive",
		"expected_version": source, "overwrite": true, "destination_must_not_exist": true,
	}))
	require.NoError(t, err)
	require.True(t, conflict.IsError)
	require.Equal(t, string(files.ErrCodeVersionConflict), behaviorJSONContent(t, conflict)["code"], "the destination condition is checked in the target project")

	invalid, err := renameTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj", "from_path": "/notes/a.txt", "to_path": "/a.txt", targetProjectKey: files.ProjectWildcard, "expected_version": source,
	}))
	require.NoError(t, err)
	require.True(t, invalid.IsError)
	require.Equal(t, string(files.ErrCodeInvalidPath), behaviorJSONContent(t, invalid)["code"], "the wildcard never selects every project")

	moved, err := renameTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "proj", "from_path": "notes/a.txt", "to_path": "inbox/a.txt", targetProjectKey: "archive", "expected_version": source,
	}))
	require.NoError(t, err)
	require.False(t, moved.IsError, behaviorTextContent(t, moved))
	require.Equal(t, 1, asInt(t, decodeToolPayload(t, moved)["moved_count"]))

	missing := read("proj", "/notes/a.txt")
	require.True(t, missing.IsError)
	require.Equal(t, string(files.ErrCodeNotFound), behaviorJSONContent(t, missing)["code"])
	arrived := read("archive", "/inbox/a.txt")
	require.False(t, arrived.IsError, behaviorTextContent(t, arrived))
	require.Equal(t, "moving bytes", decodeToolPayload(t, arrived)["content"])
	untouched := read("archive", "/taken.txt")
	require.Equal(t, "already here", decodeToolPayload(t, untouched)["content"])
	require.Equal(t, occupied, decodeToolPayload(t, untouched)["version"])

	stale, err := renameTool.Handle(authCtx, newToolReq(map[string]any{
		"project": "archive", "from_path": "/inbox/a.txt", "to_path": "/a.txt", targetProjectKey: "proj", "expected_version": source,
	}))
	require.NoError(t, err)
	require.True(t, stale.IsError)
	require.Equal(t, string(files.ErrCodeVersionConflict), behaviorJSONContent(t, stale)["code"], "the move advanced the revision")
}
