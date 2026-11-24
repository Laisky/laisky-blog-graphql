package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileToolsDefaultProjectOverlay drives the public MCP tools, with their
// mandatory version conditions, through the @DEFAULT lifecycle documented in
// docs/manual/mcp_files.md: seed a default, edit it from a project, override it
// for one project, reset the override, and keep tenants isolated.
func TestFileToolsDefaultProjectOverlay(t *testing.T) {
	svc := newE2EFileService(t, false)
	plugin := mustE2EPlugin(t, svc)
	ctx := authtest.WithVerified(context.Background(), "tenant-key")
	foreign := authtest.WithVerified(context.Background(), "foreign-key")

	tool := func(build func(FileService) (Tool, error)) Tool {
		t.Helper()
		created, err := build(plugin)
		require.NoError(t, err)
		return created
	}
	writeTool := tool(func(s FileService) (Tool, error) { return NewFileWriteTool(s) })
	readTool := tool(func(s FileService) (Tool, error) { return NewFileReadTool(s) })
	statTool := tool(func(s FileService) (Tool, error) { return NewFileStatTool(s) })
	listTool := tool(func(s FileService) (Tool, error) { return NewFileListTool(s) })
	renameTool := tool(func(s FileService) (Tool, error) { return NewFileRenameTool(s) })
	deleteTool := tool(func(s FileService) (Tool, error) { return NewFileDeleteTool(s) })

	call := func(ctx context.Context, target Tool, args map[string]any) map[string]any {
		t.Helper()
		resp, err := target.Handle(ctx, newToolReq(args))
		require.NoError(t, err)
		require.False(t, resp.IsError, "%v", resp.Content)
		return decodeToolPayload(t, resp)
	}
	callErr := func(ctx context.Context, target Tool, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		resp, err := target.Handle(ctx, newToolReq(args))
		require.NoError(t, err)
		require.True(t, resp.IsError)
		return resp
	}
	read := func(ctx context.Context, project string) map[string]any {
		t.Helper()
		return call(ctx, readTool, map[string]any{"project": project, "path": "users.md"})
	}

	// Seed a default; every project of the API key inherits it.
	call(ctx, writeTool, map[string]any{"project": files.DefaultProject, "path": "users.md", "content": "prefers tabs", "create_only": true})
	graphql := read(ctx, "graphql")
	require.Equal(t, "prefers tabs", graphql["content"])
	require.Equal(t, "prefers tabs", read(ctx, "web")["content"])

	// Editing it from any project edits the shared default.
	call(ctx, writeTool, map[string]any{
		"project": "graphql", "path": "users.md", "content": "prefers tabs; answers in English",
		"mode": "TRUNCATE", "expected_version": graphql["version"],
	})
	web := read(ctx, "web")
	require.Equal(t, "prefers tabs; answers in English", web["content"])
	require.Equal(t, web["content"], read(ctx, files.DefaultProject)["content"])

	// Override it for one project: create the project's copy under another name,
	// then rename it over the default with overwrite. The default stays global.
	draft := call(ctx, writeTool, map[string]any{"project": "web", "path": "users.md.web", "content": "prefers spaces", "create_only": true})
	call(ctx, renameTool, map[string]any{
		"project": "web", "from_path": "users.md.web", "to_path": "users.md", "overwrite": true,
		"expected_version": draft["version"], "expected_destination_version": web["version"],
	})
	override := read(ctx, "web")
	require.Equal(t, "prefers spaces", override["content"])
	require.Equal(t, "prefers tabs; answers in English", read(ctx, "graphql")["content"])
	listed := call(ctx, listTool, map[string]any{"project": "web", "path": "/", "depth": 1})
	require.Len(t, listed["entries"], 1, "the override replaces the default name instead of duplicating it")

	// Deleting the override resets the project to the default.
	call(ctx, deleteTool, map[string]any{"project": "web", "path": "users.md", "expected_version": override["version"]})
	reset := read(ctx, "web")
	require.Equal(t, "prefers tabs; answers in English", reset["content"])
	stat := call(ctx, statTool, map[string]any{"project": "web", "path": "users.md"})
	require.Equal(t, reset["version"], stat["version"])

	// Only the exact reserved name is accepted, and another key sees nothing.
	callErr(ctx, readTool, map[string]any{"project": "@default", "path": "users.md"})
	callErr(foreign, readTool, map[string]any{"project": "graphql", "path": "users.md"})
	foreignList := call(foreign, listTool, map[string]any{"project": files.DefaultProject, "path": "/", "depth": 1})
	require.Empty(t, foreignList["entries"])
}
