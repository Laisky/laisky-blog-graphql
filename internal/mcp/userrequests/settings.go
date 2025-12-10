package userrequests

import (
	"fmt"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
)

const (
	// DefaultRetentionDays is the fallback TTL for stored user requests when configuration is absent.
	DefaultRetentionDays = 30
	// DefaultRetentionSweepInterval defines how frequently the TTL pruner runs when enabled.
	DefaultRetentionSweepInterval = 6 * time.Hour
)

// Settings holds runtime configuration for the user requests service.
// RetentionDays defines how long requests are kept before TTL pruning.
type Settings struct {
	RetentionDays          int
	RetentionSweepInterval time.Duration
}

// LoadSettingsFromConfig populates Settings from the shared configuration with sensible defaults.
func LoadSettingsFromConfig() Settings {
	retentionDays := intFromConfig("settings.mcp.user_requests.retention_days", DefaultRetentionDays)
	intervalSeconds := intFromConfig("settings.mcp.user_requests.retention_sweep_seconds", int(DefaultRetentionSweepInterval/time.Second))
	return Settings{
		RetentionDays:          retentionDays,
		RetentionSweepInterval: time.Duration(intervalSeconds) * time.Second,
	}
}

// intFromConfig retrieves an integer configuration value with a default fallback.
func intFromConfig(key string, def int) int {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}
