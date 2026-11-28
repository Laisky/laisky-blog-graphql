package cmd

import (
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

const (
	// pageIndexSettingsKey is the configuration block of the disabled PageIndex plugin.
	pageIndexSettingsKey = "settings.mcp.tools.memory.plugins.pageindex"
	// removedShadowReplayEnabledKey is the toggle of the removed memory-plugin
	// shadow replay. Replay sent every mutation through a second plugin, but the
	// plugins share one FileIO service, so a replayed write changed the same
	// physical file twice. Every settings.mcp.tools.memory.shadow.* key is ignored.
	removedShadowReplayEnabledKey = "settings.mcp.tools.memory.shadow.enabled"
)

// newMCPFileManager builds the MCP file plugin manager over rag, the only
// supported memory backend. The plugin is registered as itself, with no
// wrapper, recorder or background worker, so a file operation runs once.
// Stale configuration of disabled or removed features never constructs
// anything and never fails startup; each case logs one warning, and no
// configured value is logged:
//   - a settings.mcp.tools.memory.plugins.pageindex block, including its API key;
//   - default_plugin: pageindex, which routes to rag;
//   - settings.mcp.tools.memory.shadow.enabled=true.
func newMCPFileManager(get configGetter, logger logSDK.Logger, configuredDefault string, rag mcpplugin.Plugin) (*mcpplugin.Manager, error) {
	if get(pageIndexSettingsKey) != nil {
		logger.Warn("ignoring the pageindex plugin configuration: pageindex is disabled and rag is the supported memory backend",
			zap.String("config_key", pageIndexSettingsKey))
	}
	defaultPlugin := mcpplugin.NormalizeName(configuredDefault)
	if defaultPlugin == mcpplugin.DefaultPluginPageIndex {
		logger.Warn("ignoring default_plugin pageindex: pageindex is disabled, routing to rag",
			zap.String("config_key", "settings.mcp.tools.memory.default_plugin"))
		defaultPlugin = mcpplugin.DefaultPluginRAG
	}
	if enabled, _ := parseStrictBool(get(removedShadowReplayEnabledKey)); enabled {
		logger.Warn("memory plugin shadow replay was removed; the setting is ignored",
			zap.String("setting", removedShadowReplayEnabledKey))
	}
	return mcpplugin.NewManager(defaultPlugin, rag)
}
