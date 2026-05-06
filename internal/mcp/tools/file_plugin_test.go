package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// pluginBehaviorService adapts behaviorFileService to the plugin contract for routing tests.
type pluginBehaviorService struct {
	*behaviorFileService
	name string
}

// Name returns the plugin name for routing tests.
func (s *pluginBehaviorService) Name() string {
	return s.name
}

// Capabilities returns stub capabilities for routing tests.
func (s *pluginBehaviorService) Capabilities() mcpplugin.Capabilities {
	return mcpplugin.Capabilities{FreshnessWindow: time.Second}
}

// Start is a no-op for routing tests.
func (s *pluginBehaviorService) Start(context.Context) error {
	return nil
}

// Stop is a no-op for routing tests.
func (s *pluginBehaviorService) Stop(context.Context) error {
	return nil
}

// TestFileToolDefinitionsIncludePluginField verifies the additive-only schema change.
func TestFileToolDefinitionsIncludePluginField(t *testing.T) {
	t.Parallel()

	svc := &behaviorFileService{}
	checks := []Tool{
		mustFileStatTool(t, svc),
		mustFileReadTool(t, svc),
		mustFileWriteTool(t, svc),
		mustFileDeleteTool(t, svc),
		mustFileRenameTool(t, svc),
		mustFileListTool(t, svc),
		mustFileSearchTool(t, svc),
	}

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

// TestFileToolUnknownPluginReturnsStructuredError verifies manager routing failures stay user-correctable.
func TestFileToolUnknownPluginReturnsStructuredError(t *testing.T) {
	t.Parallel()

	routedSvc := &pluginBehaviorService{
		behaviorFileService: &behaviorFileService{statResult: files.StatResult{Exists: true}},
		name:                mcpplugin.DefaultPluginRAG,
	}
	manager, err := mcpplugin.NewManager(mcpplugin.DefaultPluginRAG, routedSvc)
	require.NoError(t, err)

	tool, err := NewFileStatTool(manager)
	require.NoError(t, err)

	result, err := tool.Handle(behaviorAuthCtx(), behaviorReq(map[string]any{
		"project": "demo",
		"path":    "/doc.txt",
		"plugin":  mcpplugin.DefaultPluginPageIndex,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)

	payload := behaviorJSONContent(t, result)
	require.Equal(t, string(files.ErrCodeInvalidArgument), payload["code"])
	require.Contains(t, payload["message"], mcpplugin.DefaultPluginPageIndex)
	require.Equal(t, []any{mcpplugin.DefaultPluginRAG}, payload["available_plugins"])
}
