package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// TestMemoryToolDefinitionsIncludePluginField verifies memory lifecycle tools expose the additive plugin selector.
func TestMemoryToolDefinitionsIncludePluginField(t *testing.T) {
	t.Parallel()

	svc := newTestToolMemoryService(t)
	beforeTool, err := NewMemoryBeforeTurnTool(svc)
	require.NoError(t, err)
	afterTool, err := NewMemoryAfterTurnTool(svc)
	require.NoError(t, err)
	maintenanceTool, err := NewMemoryRunMaintenanceTool(svc)
	require.NoError(t, err)
	listTool, err := NewMemoryListDirWithAbstractTool(svc)
	require.NoError(t, err)

	checks := []Tool{beforeTool, afterTool, maintenanceTool, listTool}
	for _, tool := range checks {
		def := tool.Definition()
		rawProperty, ok := def.InputSchema.Properties["plugin"]
		require.True(t, ok, def.Name)

		property, ok := rawProperty.(map[string]any)
		require.True(t, ok, def.Name)
		require.Equal(t, "string", property["type"])
		require.Equal(t, "auto", property["default"])
		require.Equal(t, []string{"rag", "pageindex", "auto"}, property["enum"])
	}
}

// TestMemoryBeforeTurnUnknownPluginReturnsStructuredError verifies invalid plugin routing stays user-correctable.
func TestMemoryBeforeTurnUnknownPluginReturnsStructuredError(t *testing.T) {
	t.Parallel()

	svc := newTestToolMemoryService(t)
	tool, err := NewMemoryBeforeTurnTool(svc)
	require.NoError(t, err)
	const invalidPlugin = "bogus-plugin"

	result, err := tool.Handle(behaviorAuthCtx(), behaviorReq(map[string]any{
		"project":       "demo",
		"session_id":    "plugin-routing",
		"current_input": "hello memory",
		"plugin":        invalidPlugin,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)

	payload := behaviorJSONContent(t, result)
	require.Equal(t, string(mcpmemory.ErrCodeInvalidArgument), payload["code"])
	require.Contains(t, payload["message"], invalidPlugin)
	require.NotEmpty(t, payload["available_plugins"])
}
