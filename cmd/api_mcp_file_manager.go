package cmd

import (
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// removedShadowReplayEnabledKey is the toggle of the removed memory-plugin
// shadow replay. Replay sent every mutation through a second plugin, but RAG
// and PageIndex share one FileIO service, so a replayed write changed the same
// physical file twice. This key and every other
// settings.mcp.tools.memory.shadow.* key are ignored.
const removedShadowReplayEnabledKey = "settings.mcp.tools.memory.shadow.enabled"

// newMCPFileManager registers the MCP file plugins with the routing manager.
// Each plugin is registered as itself, with no wrapper, recorder or background
// worker, so a file operation runs once, on the one plugin the call selects.
// A stale settings.mcp.tools.memory.shadow.enabled=true only logs one warning:
// it neither enables replay nor fails startup.
func newMCPFileManager(get configGetter, logger logSDK.Logger, defaultPlugin string, plugins ...mcpplugin.Plugin) (*mcpplugin.Manager, error) {
	if enabled, _ := parseStrictBool(get(removedShadowReplayEnabledKey)); enabled {
		logger.Warn("memory plugin shadow replay was removed; the setting is ignored",
			zap.String("setting", removedShadowReplayEnabledKey))
	}

	return mcpplugin.NewManager(defaultPlugin, plugins...)
}
