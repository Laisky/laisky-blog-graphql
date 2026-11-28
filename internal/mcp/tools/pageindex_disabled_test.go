package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// requirePageIndexDisabled asserts the shared structured error for an
// explicit pageindex selection, without the retired configuration hint.
func requirePageIndexDisabled(t *testing.T, payload map[string]any) {
	t.Helper()
	require.Equal(t, string(files.ErrCodeInvalidArgument), payload["code"])
	require.Equal(t, mcpplugin.PageIndexDisabledMessage, payload["message"])
	require.Equal(t, false, payload["retryable"])
	require.Equal(t, []any{mcpplugin.DefaultPluginRAG}, payload["available_plugins"])
	require.NotContains(t, payload["message"], "api_key")
}

// TestFileToolsRefuseDisabledPageIndex verifies that an explicit
// plugin=pageindex on file tools fails with INVALID_ARGUMENT and reaches no
// backend, while omitting the selection still routes to rag.
func TestFileToolsRefuseDisabledPageIndex(t *testing.T) {
	t.Parallel()

	routed := &pluginBehaviorService{
		behaviorFileService: &behaviorFileService{statResult: files.StatResult{Exists: true}},
		name:                mcpplugin.DefaultPluginRAG,
	}
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, routed)
	require.NoError(t, err)
	stat, err := NewFileStatTool(manager)
	require.NoError(t, err)
	rename, err := NewFileRenameTool(manager)
	require.NoError(t, err)

	for _, call := range []struct {
		tool Tool
		args map[string]any
	}{
		{stat, map[string]any{"project": "demo", "path": "/doc.md"}},
		{rename, map[string]any{"project": "demo", "from_path": "/doc.md", "to_path": "/doc.pdf", "expected_version": testRenameVersion}},
	} {
		call.args["plugin"] = "PageIndex"
		result, err := call.tool.Handle(behaviorAuthCtx(), behaviorReq(call.args))
		require.NoError(t, err)
		require.True(t, result.IsError, call.tool.Definition().Name)
		requirePageIndexDisabled(t, behaviorJSONContent(t, result))
	}
	require.Empty(t, routed.lastProject, "a refused selection never reaches a backend")
	require.Zero(t, routed.lastRename)

	result, err := stat.Handle(behaviorAuthCtx(), behaviorReq(map[string]any{"project": "demo", "path": "/doc.md"}))
	require.NoError(t, err)
	require.False(t, result.IsError)
}

// TestMemoryToolRefusesDisabledPageIndex verifies the memory lifecycle tools
// map an explicit pageindex selection onto the same structured error.
func TestMemoryToolRefusesDisabledPageIndex(t *testing.T) {
	t.Parallel()

	tool, err := NewMemoryBeforeTurnTool(newTestToolMemoryService(t))
	require.NoError(t, err)
	result, err := tool.Handle(behaviorAuthCtx(), behaviorReq(map[string]any{
		"project":       "demo",
		"session_id":    "pageindex-disabled",
		"current_input": "hello memory",
		"plugin":        mcpplugin.DefaultPluginPageIndex,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	payload := behaviorJSONContent(t, result)
	require.Equal(t, string(mcpmemory.ErrCodeInvalidArgument), payload["code"])
	requirePageIndexDisabled(t, payload)
}
