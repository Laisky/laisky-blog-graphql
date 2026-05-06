package plugin

import (
	"strings"

	gconfig "github.com/Laisky/go-config/v2"
)

// Settings captures manager-level plugin routing configuration.
type Settings struct {
	DefaultPlugin string
}

// LoadSettingsFromConfig reads the MCP memory plugin manager settings.
func LoadSettingsFromConfig() Settings {
	defaultPlugin := NormalizeName(strings.TrimSpace(gconfig.S.GetString("settings.mcp.tools.memory.default_plugin")))
	if defaultPlugin == "" {
		defaultPlugin = DefaultPluginRAG
	}

	return Settings{DefaultPlugin: defaultPlugin}
}
