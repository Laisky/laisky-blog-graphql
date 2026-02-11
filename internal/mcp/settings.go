// Package mcp provides MCP server implementations and tools.
package mcp

import (
	gconfig "github.com/Laisky/go-config/v2"
)

// ToolsSettings captures runtime configuration for enabling or disabling individual MCP tools.
type ToolsSettings struct {
	WebSearchEnabled      bool
	WebFetchEnabled       bool
	AskUserEnabled        bool
	GetUserRequestEnabled bool
	ExtractKeyInfoEnabled bool
	FileIOEnabled         bool
	MCPPipeEnabled        bool
}

// LoadToolsSettingsFromConfig reads the MCP tools configuration and returns a ToolsSettings instance.
// By default, all tools are enabled unless explicitly disabled in the configuration.
func LoadToolsSettingsFromConfig() ToolsSettings {
	return ToolsSettings{
		WebSearchEnabled:      boolFromConfig("settings.mcp.tools.web_search.enabled", true),
		WebFetchEnabled:       boolFromConfig("settings.mcp.tools.web_fetch.enabled", true),
		AskUserEnabled:        boolFromConfig("settings.mcp.tools.ask_user.enabled", true),
		GetUserRequestEnabled: boolFromConfig("settings.mcp.tools.get_user_request.enabled", true),
		ExtractKeyInfoEnabled: boolFromConfig("settings.mcp.tools.extract_key_info.enabled", true),
		FileIOEnabled:         boolFromConfig("settings.mcp.tools.file_io.enabled", true),
		MCPPipeEnabled:        boolFromConfig("settings.mcp.tools.mcp_pipe.enabled", true),
	}
}

// boolFromConfig retrieves a boolean configuration value with a default fallback.
func boolFromConfig(key string, def bool) bool {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		switch v {
		case "true", "True", "TRUE", "1", "yes", "Yes", "YES":
			return true
		case "false", "False", "FALSE", "0", "no", "No", "NO":
			return false
		default:
			return def
		}
	default:
		return def
	}
}
