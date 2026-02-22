package memory

import (
	"strings"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
)

const (
	defaultRecentContextItems       = 30
	defaultRecallFactsLimit         = 20
	defaultSearchLimit              = 5
	defaultCompactThreshold         = 0.8
	defaultL1RetentionDays          = 1
	defaultL2RetentionDays          = 7
	defaultCompactionMinAgeHours    = 24
	defaultSummaryRefreshMinutes    = 60
	defaultMaxProcessedTurns        = 1024
	defaultHeuristicModel           = "openai/gpt-oss-120b"
	defaultHeuristicTimeoutMS       = 12000
	defaultHeuristicMaxOutputTokens = 800
	defaultSessionLockTimeoutMS     = 5000
)

// Settings controls MCP-native memory behavior.
type Settings struct {
	RecentContextItems     int
	RecallFactsLimit       int
	SearchLimit            int
	CompactThreshold       float64
	L1RetentionDays        int
	L2RetentionDays        int
	CompactionMinAge       time.Duration
	SummaryRefreshInterval time.Duration
	MaxProcessedTurns      int
	SessionLockTimeout     time.Duration
	Heuristic              HeuristicSettings
}

// HeuristicSettings controls optional model-assisted fact extraction.
type HeuristicSettings struct {
	Enabled         bool
	Model           string
	BaseURL         string
	Timeout         time.Duration
	MaxOutputTokens int
}

// LoadSettingsFromConfig loads memory settings with safe defaults.
func LoadSettingsFromConfig() Settings {
	timeoutMS := intFromConfig("settings.mcp.memory.heuristic.timeout_ms", defaultHeuristicTimeoutMS)
	if timeoutMS <= 0 {
		timeoutMS = defaultHeuristicTimeoutMS
	}

	return Settings{
		RecentContextItems:     intFromConfig("settings.mcp.memory.recent_context_items", defaultRecentContextItems),
		RecallFactsLimit:       intFromConfig("settings.mcp.memory.recall_facts_limit", defaultRecallFactsLimit),
		SearchLimit:            intFromConfig("settings.mcp.memory.search_limit", defaultSearchLimit),
		CompactThreshold:       floatFromConfig("settings.mcp.memory.compact_threshold", defaultCompactThreshold),
		L1RetentionDays:        intFromConfig("settings.mcp.memory.l1_retention_days", defaultL1RetentionDays),
		L2RetentionDays:        intFromConfig("settings.mcp.memory.l2_retention_days", defaultL2RetentionDays),
		CompactionMinAge:       time.Duration(intFromConfig("settings.mcp.memory.compaction_min_age_hours", defaultCompactionMinAgeHours)) * time.Hour,
		SummaryRefreshInterval: time.Duration(intFromConfig("settings.mcp.memory.summary_refresh_interval_minutes", defaultSummaryRefreshMinutes)) * time.Minute,
		MaxProcessedTurns:      intFromConfig("settings.mcp.memory.max_processed_turns", defaultMaxProcessedTurns),
		SessionLockTimeout:     time.Duration(intFromConfig("settings.mcp.memory.session_lock_timeout_ms", defaultSessionLockTimeoutMS)) * time.Millisecond,
		Heuristic: HeuristicSettings{
			Enabled:         boolFromConfig("settings.mcp.memory.heuristic.enabled", false),
			Model:           stringFromConfig("settings.mcp.memory.heuristic.model", defaultHeuristicModel),
			BaseURL:         stringFromConfig("settings.mcp.memory.heuristic.base_url", "https://oneapi.laisky.com"),
			Timeout:         time.Duration(timeoutMS) * time.Millisecond,
			MaxOutputTokens: intFromConfig("settings.mcp.memory.heuristic.max_output_tokens", defaultHeuristicMaxOutputTokens),
		},
	}
}

// boolFromConfig reads a boolean from config with fallback.
func boolFromConfig(key string, def bool) bool {
	value := gconfig.S.Get(key)
	switch typed := value.(type) {
	case nil:
		return def
	case bool:
		return typed
	default:
		return def
	}
}

// intFromConfig reads an integer from config with fallback.
func intFromConfig(key string, def int) int {
	value := gconfig.S.Get(key)
	switch typed := value.(type) {
	case nil:
		return def
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return def
	}
}

// floatFromConfig reads a float from config with fallback.
func floatFromConfig(key string, def float64) float64 {
	value := gconfig.S.Get(key)
	switch typed := value.(type) {
	case nil:
		return def
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return def
	}
}

// stringFromConfig reads a string from config with fallback.
func stringFromConfig(key string, def string) string {
	value := gconfig.S.Get(key)
	typed, ok := value.(string)
	if !ok {
		return def
	}
	trimmed := strings.TrimSpace(typed)
	if trimmed == "" {
		return def
	}
	return trimmed
}
