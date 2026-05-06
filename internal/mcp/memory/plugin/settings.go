package plugin

import (
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
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

// ShadowSettings captures the proposal §8 Phase 3 dual-write opt-in. The
// wrapper is off by default; operators flip Enabled and pin a single global
// live/shadow pair to begin shadow replay. Per-project, per-tenant, and
// per-API-key routing layers were considered and rejected in proposal §2.4 —
// the only caller-controlled selector is the optional per-call `plugin`
// argument with `default_plugin` as the global fallback. Shadow replay
// therefore wraps the live plugin globally; agents that need to bypass shadow
// capture for a specific call simply pass `plugin="<live>"` (no-op since
// shadow follows the live route).
type ShadowSettings struct {
	Enabled      bool
	LivePlugin   string
	ShadowPlugin string
	RecorderPath string
	Concurrency  int
	OpTimeout    time.Duration
	DrainGrace   time.Duration
}

// LoadShadowSettingsFromConfig reads the optional shadow-replay block.
func LoadShadowSettingsFromConfig() ShadowSettings {
	const prefix = "settings.mcp.tools.memory.shadow"
	return ShadowSettings{
		Enabled:      gconfig.S.GetBool(prefix + ".enabled"),
		LivePlugin:   NormalizeName(gconfig.S.GetString(prefix + ".live_plugin")),
		ShadowPlugin: NormalizeName(gconfig.S.GetString(prefix + ".shadow_plugin")),
		RecorderPath: strings.TrimSpace(gconfig.S.GetString(prefix + ".recorder_path")),
		Concurrency:  gconfig.S.GetInt(prefix + ".concurrency"),
		OpTimeout:    gconfig.S.GetDuration(prefix + ".op_timeout"),
		DrainGrace:   gconfig.S.GetDuration(prefix + ".drain_grace"),
	}
}

// Validate checks that an enabled ShadowSettings carries the fields required to
// wire ShadowPlugin without ambiguity.
func (s ShadowSettings) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.LivePlugin == "" {
		return errors.New("live_plugin is required when shadow is enabled")
	}
	if s.ShadowPlugin == "" {
		return errors.New("shadow_plugin is required when shadow is enabled")
	}
	if s.LivePlugin == s.ShadowPlugin {
		return errors.New("live_plugin and shadow_plugin must differ")
	}
	if s.RecorderPath == "" {
		return errors.New("recorder_path is required when shadow is enabled")
	}
	return nil
}
