package files

import (
	"fmt"
	"math"
	"strings"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
)

// Settings captures runtime configuration for FileIO features.
type Settings struct {
	AllowRootWipe    bool
	MaxPayloadBytes  int64
	MaxFileBytes     int64
	MaxProjectBytes  int64
	ListLimitDefault int
	ListLimitMax     int
	LockTimeout      time.Duration
	DeleteRetention  time.Duration
	EmbeddingModel   string
	EmbeddingBaseURL string
	Search           SearchSettings
	Index            IndexSettings
	Security         SecuritySettings
}

// SearchSettings captures query-time configuration for file_search.
type SearchSettings struct {
	Enabled           bool
	LimitDefault      int
	LimitMax          int
	VectorCandidates  int
	LexicalCandidates int
	RerankModel       string
	RerankEndpoint    string
	RerankTimeout     time.Duration
	SemanticWeight    float64
	LexicalWeight     float64
}

// IndexSettings configures index worker behavior.
type IndexSettings struct {
	Workers      int
	BatchSize    int
	RetryMax     int
	RetryBackoff time.Duration
	ChunkBytes   int
	FreshnessSLO time.Duration
}

// SecuritySettings configures credential handoff encryption and cache usage.
type SecuritySettings struct {
	EncryptionKey         string
	EncryptionKEKID       uint16
	CredentialCachePrefix string
	CredentialCacheTTL    time.Duration
}

// LoadSettingsFromConfig reads configuration and applies safe defaults.
func LoadSettingsFromConfig() Settings {
	settings := Settings{
		AllowRootWipe:    gconfig.S.GetBool("settings.mcp.files.allow_root_wipe"),
		MaxPayloadBytes:  int64FromConfig("settings.mcp.files.max_payload_bytes", 2_000_000),
		MaxFileBytes:     int64FromConfig("settings.mcp.files.max_file_bytes", 10_000_000),
		MaxProjectBytes:  int64FromConfig("settings.mcp.files.max_project_bytes", 100_000_000),
		ListLimitDefault: intFromConfig("settings.mcp.files.list_limit_default", 256),
		ListLimitMax:     intFromConfig("settings.mcp.files.list_limit_max", 1024),
		LockTimeout:      time.Duration(intFromConfig("settings.mcp.files.lock_timeout_ms", 3000)) * time.Millisecond,
		DeleteRetention:  time.Duration(intFromConfig("settings.mcp.files.delete_retention_days", 30)) * 24 * time.Hour,
		EmbeddingModel:   strings.TrimSpace(gconfig.S.GetString("settings.openai.embedding_model")),
		EmbeddingBaseURL: strings.TrimSpace(gconfig.S.GetString("settings.openai.base_url")),
		Search: SearchSettings{
			Enabled:           boolFromConfig("settings.mcp.files.search.enabled", true),
			LimitDefault:      intFromConfig("settings.mcp.files.search.limit_default", 5),
			LimitMax:          intFromConfig("settings.mcp.files.search.limit_max", 20),
			VectorCandidates:  intFromConfig("settings.mcp.files.search.vector_candidates", 30),
			LexicalCandidates: intFromConfig("settings.mcp.files.search.bm25_candidates", 30),
			RerankModel:       strings.TrimSpace(gconfig.S.GetString("settings.mcp.files.search.rerank.model")),
			RerankEndpoint:    strings.TrimSpace(gconfig.S.GetString("settings.mcp.files.search.rerank.endpoint")),
			RerankTimeout:     time.Duration(intFromConfig("settings.mcp.files.search.rerank.timeout_ms", 6000)) * time.Millisecond,
			SemanticWeight:    floatFromConfig("settings.mcp.files.search.fallback.semantic_weight", 0.65),
			LexicalWeight:     floatFromConfig("settings.mcp.files.search.fallback.lexical_weight", 0.35),
		},
		Index: IndexSettings{
			Workers:      intFromConfig("settings.mcp.files.index.workers", 2),
			BatchSize:    intFromConfig("settings.mcp.files.index.batch_size", 20),
			RetryMax:     intFromConfig("settings.mcp.files.index.retry_max", 5),
			RetryBackoff: time.Duration(intFromConfig("settings.mcp.files.index.retry_backoff_ms", 1000)) * time.Millisecond,
			ChunkBytes:   intFromConfig("settings.mcp.files.index.chunk_bytes", 1500),
			FreshnessSLO: time.Duration(intFromConfig("settings.mcp.files.index.slo_p95_seconds", 30)) * time.Second,
		},
		Security: SecuritySettings{
			EncryptionKey:         strings.TrimSpace(gconfig.S.GetString("settings.mcp.files.security.encryption_key")),
			EncryptionKEKID:       uint16(intFromConfig("settings.mcp.files.security.encryption_kek_id", 1)),
			CredentialCachePrefix: strings.TrimSpace(gconfig.S.GetString("settings.mcp.files.security.credential_cache_prefix")),
			CredentialCacheTTL:    time.Duration(intFromConfig("settings.mcp.files.security.credential_cache_ttl_seconds", 300)) * time.Second,
		},
	}

	if settings.MaxPayloadBytes <= 0 {
		settings.MaxPayloadBytes = 2_000_000
	}
	if settings.MaxFileBytes <= 0 {
		settings.MaxFileBytes = 10_000_000
	}
	if settings.MaxProjectBytes <= 0 {
		settings.MaxProjectBytes = 100_000_000
	}
	if settings.ListLimitDefault <= 0 {
		settings.ListLimitDefault = 256
	}
	if settings.ListLimitMax <= 0 {
		settings.ListLimitMax = 1024
	}
	if settings.ListLimitDefault > settings.ListLimitMax {
		settings.ListLimitDefault = settings.ListLimitMax
	}
	if settings.LockTimeout <= 0 {
		settings.LockTimeout = 3 * time.Second
	}
	if settings.DeleteRetention <= 0 {
		settings.DeleteRetention = 30 * 24 * time.Hour
	}
	if settings.EmbeddingModel == "" {
		settings.EmbeddingModel = "text-embedding-3-small"
	}
	if settings.Search.LimitDefault <= 0 {
		settings.Search.LimitDefault = 5
	}
	if settings.Search.LimitMax <= 0 {
		settings.Search.LimitMax = 20
	}
	if settings.Search.LimitDefault > settings.Search.LimitMax {
		settings.Search.LimitDefault = settings.Search.LimitMax
	}
	if settings.Search.VectorCandidates <= 0 {
		settings.Search.VectorCandidates = 30
	}
	if settings.Search.LexicalCandidates <= 0 {
		settings.Search.LexicalCandidates = 30
	}
	if settings.Search.RerankModel == "" {
		settings.Search.RerankModel = "rerank-v3.5"
	}
	if settings.Search.RerankEndpoint == "" {
		settings.Search.RerankEndpoint = "https://oneapi.laisky.com/v1/rerank"
	}
	if settings.Search.RerankTimeout <= 0 {
		settings.Search.RerankTimeout = 6 * time.Second
	}
	settings.Search.SemanticWeight, settings.Search.LexicalWeight = normalizeWeights(settings.Search.SemanticWeight, settings.Search.LexicalWeight)
	if settings.Index.Workers <= 0 {
		settings.Index.Workers = 1
	}
	if settings.Index.BatchSize <= 0 {
		settings.Index.BatchSize = 10
	}
	if settings.Index.RetryMax < 0 {
		settings.Index.RetryMax = 0
	}
	if settings.Index.RetryBackoff <= 0 {
		settings.Index.RetryBackoff = time.Second
	}
	if settings.Index.ChunkBytes <= 0 {
		settings.Index.ChunkBytes = 1500
	}
	if settings.Index.FreshnessSLO <= 0 {
		settings.Index.FreshnessSLO = 30 * time.Second
	}
	if settings.Security.CredentialCachePrefix == "" {
		settings.Security.CredentialCachePrefix = "mcp:files:cred"
	}
	if settings.Security.CredentialCacheTTL <= 0 {
		settings.Security.CredentialCacheTTL = 300 * time.Second
	}

	return settings
}

// intFromConfig reads an int configuration value with a default fallback.
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
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return def
		}
		var parsed int
		_, err := fmt.Sscanf(trimmed, "%d", &parsed)
		if err != nil {
			return def
		}
		return parsed
	default:
		return def
	}
}

// int64FromConfig reads an int64 configuration value with a default fallback.
func int64FromConfig(key string, def int64) int64 {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return def
		}
		var parsed int64
		_, err := fmt.Sscanf(trimmed, "%d", &parsed)
		if err != nil {
			return def
		}
		return parsed
	default:
		return def
	}
}

// boolFromConfig reads a boolean configuration value with a default fallback.
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

// floatFromConfig reads a float64 configuration value with a default fallback.
func floatFromConfig(key string, def float64) float64 {
	value := gconfig.S.Get(key)
	switch v := value.(type) {
	case nil:
		return def
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return def
		}
		var parsed float64
		_, err := fmt.Sscanf(trimmed, "%f", &parsed)
		if err != nil {
			return def
		}
		return parsed
	default:
		return def
	}
}

// normalizeWeights ensures semantic and lexical weights are normalized.
func normalizeWeights(semantic, lexical float64) (float64, float64) {
	if semantic <= 0 {
		semantic = 0.65
	}
	if lexical <= 0 {
		lexical = 0.35
	}
	total := semantic + lexical
	if total == 0 {
		return 0.65, 0.35
	}
	if math.Abs(total-1) > 0.01 {
		semantic = semantic / total
		lexical = lexical / total
	}
	return semantic, lexical
}
