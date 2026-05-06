package files

import (
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// TestLoadSettingsFromConfigPrefersNewRAGPath verifies the new config tree overrides the legacy shim.
func TestLoadSettingsFromConfigPrefersNewRAGPath(t *testing.T) {
	t.Parallel()

	legacyListLimitKey := "settings.mcp.files.list_limit_default"
	newListLimitKey := "settings.mcp.tools.memory.plugins.rag.list_limit_default"
	oldLegacy := gconfig.Shared.Get(legacyListLimitKey)
	oldNew := gconfig.Shared.Get(newListLimitKey)
	defer func() {
		gconfig.Shared.Set(legacyListLimitKey, oldLegacy)
		gconfig.Shared.Set(newListLimitKey, oldNew)
	}()

	gconfig.Shared.Set(legacyListLimitKey, 11)
	gconfig.Shared.Set(newListLimitKey, 22)

	settings := LoadSettingsFromConfig()
	require.Equal(t, 22, settings.ListLimitDefault)
}

// TestLegacyConfigConfigured verifies the startup warning gate for the deprecated config tree.
func TestLegacyConfigConfigured(t *testing.T) {
	t.Parallel()

	legacyRootKey := "settings.mcp.files"
	oldLegacy := gconfig.Shared.Get(legacyRootKey)
	defer gconfig.Shared.Set(legacyRootKey, oldLegacy)

	gconfig.Shared.Set(legacyRootKey, map[string]any{"list_limit_default": 12})
	require.True(t, LegacyConfigConfigured())
}
