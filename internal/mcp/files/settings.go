package files

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
)

// Sentinel errors for parseUint16.
var (
	errOutOfRange      = errors.New("out of range")
	errEmpty           = errors.New("empty")
	errUnsupportedType = errors.New("unsupported type")
)

const (
	legacyFilesConfigPrefix = "settings.mcp.files"
	ragFilesConfigPrefix    = "settings.mcp.tools.memory.plugins.rag"
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
	// EnforceSummary is the rollout enforcement gate for the file-summary contract
	// (docs/proposals/file_search_file_summaries.md §3.3, §4.7). When enabled, the
	// raw-file fallback must not return a hit whose current content generation lacks
	// a ready or degraded summary. Default false preserves pre-rollout behavior.
	EnforceSummary bool
}

// IndexSettings configures index worker behavior.
type IndexSettings struct {
	Workers        int
	BatchSize      int
	RetryMax       int
	RetryBackoff   time.Duration
	ChunkBytes     int
	SummaryModel   string
	SummaryBaseURL string
	SummaryTimeout time.Duration
	FreshnessSLO   time.Duration
	// FileSummary configures the file-level overview published with each search hit.
	// It is separate from the SummaryModel/BaseURL/Timeout fields above, which drive
	// the per-chunk contextualizer (§4.4, §6.4).
	FileSummary FileSummarySettings
}

// FileSummarySettings configures the file-level summary producer (§6.4).
type FileSummarySettings struct {
	Enabled              bool
	Model                string
	BaseURL              string
	Timeout              time.Duration
	TargetWords          int
	MaxWords             int
	MaxBytes             int
	MaxInputTokens       int
	MaxReduceCalls       int
	MaxTotalInputTokens  int
	MaxTotalOutputTokens int
	PromptVersion        string
}

// SecuritySettings configures credential handoff encryption and cache usage.
type SecuritySettings struct {
	EncryptionKEKs        map[uint16]string
	CredentialCachePrefix string
	CredentialCacheTTL    time.Duration
}

// KEKs returns all configured non-empty KEKs.
func (s SecuritySettings) KEKs() map[uint16]string {
	keks := make(map[uint16]string, len(s.EncryptionKEKs))
	for id, key := range s.EncryptionKEKs {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		keks[id] = trimmed
	}
	return keks
}

// LoadSettingsFromConfig reads configuration and applies safe defaults.
func LoadSettingsFromConfig() Settings {
	settings := Settings{
		AllowRootWipe:    gconfig.S.GetBool(configKeyWithFallback(ragFilesConfigKey("allow_root_wipe"), legacyFilesConfigKey("allow_root_wipe"))),
		MaxPayloadBytes:  int64FromConfig(configKeyWithFallback(ragFilesConfigKey("max_payload_bytes"), legacyFilesConfigKey("max_payload_bytes")), 2_000_000),
		MaxFileBytes:     int64FromConfig(configKeyWithFallback(ragFilesConfigKey("max_file_bytes"), legacyFilesConfigKey("max_file_bytes")), 10_000_000),
		MaxProjectBytes:  int64FromConfig(configKeyWithFallback(ragFilesConfigKey("max_project_bytes"), legacyFilesConfigKey("max_project_bytes")), 100_000_000),
		ListLimitDefault: intFromConfig(configKeyWithFallback(ragFilesConfigKey("list_limit_default"), legacyFilesConfigKey("list_limit_default")), 256),
		ListLimitMax:     intFromConfig(configKeyWithFallback(ragFilesConfigKey("list_limit_max"), legacyFilesConfigKey("list_limit_max")), 1024),
		LockTimeout:      time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("lock_timeout_ms"), legacyFilesConfigKey("lock_timeout_ms")), 3000)) * time.Millisecond,
		DeleteRetention:  time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("delete_retention_days"), legacyFilesConfigKey("delete_retention_days")), 30)) * 24 * time.Hour,
		EmbeddingModel:   strings.TrimSpace(gconfig.S.GetString("settings.openai.embedding_model")),
		EmbeddingBaseURL: strings.TrimSpace(gconfig.S.GetString("settings.openai.base_url")),
		Search: SearchSettings{
			Enabled:           boolFromConfig(configKeyWithFallback(ragFilesConfigKey("search.enabled"), legacyFilesConfigKey("search.enabled")), true),
			LimitDefault:      intFromConfig(configKeyWithFallback(ragFilesConfigKey("search.limit_default"), legacyFilesConfigKey("search.limit_default")), 20),
			LimitMax:          intFromConfig(configKeyWithFallback(ragFilesConfigKey("search.limit_max"), legacyFilesConfigKey("search.limit_max")), 20),
			VectorCandidates:  intFromConfig(configKeyWithFallback(ragFilesConfigKey("search.vector_candidates"), legacyFilesConfigKey("search.vector_candidates")), 100),
			LexicalCandidates: intFromConfig(configKeyWithFallback(ragFilesConfigKey("search.bm25_candidates"), legacyFilesConfigKey("search.bm25_candidates")), 100),
			RerankModel:       strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("search.rerank.model"), legacyFilesConfigKey("search.rerank.model")))),
			RerankEndpoint:    strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("search.rerank.endpoint"), legacyFilesConfigKey("search.rerank.endpoint")))),
			RerankTimeout:     time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("search.rerank.timeout_ms"), legacyFilesConfigKey("search.rerank.timeout_ms")), 6000)) * time.Millisecond,
			SemanticWeight:    floatFromConfig(configKeyWithFallback(ragFilesConfigKey("search.fallback.semantic_weight"), legacyFilesConfigKey("search.fallback.semantic_weight")), 0.65),
			LexicalWeight:     floatFromConfig(configKeyWithFallback(ragFilesConfigKey("search.fallback.lexical_weight"), legacyFilesConfigKey("search.fallback.lexical_weight")), 0.35),
			EnforceSummary:    boolFromConfig(configKeyWithFallback(ragFilesConfigKey("search.enforce_summary"), legacyFilesConfigKey("search.enforce_summary")), false),
		},
		Index: IndexSettings{
			Workers:        intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.workers"), legacyFilesConfigKey("index.workers")), 2),
			BatchSize:      intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.batch_size"), legacyFilesConfigKey("index.batch_size")), 20),
			RetryMax:       intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.retry_max"), legacyFilesConfigKey("index.retry_max")), 5),
			RetryBackoff:   time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.retry_backoff_ms"), legacyFilesConfigKey("index.retry_backoff_ms")), 1000)) * time.Millisecond,
			ChunkBytes:     intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.chunk_bytes"), legacyFilesConfigKey("index.chunk_bytes")), 500),
			SummaryModel:   strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("index.summary.model"), legacyFilesConfigKey("index.summary.model")))),
			SummaryBaseURL: strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("index.summary.base_url"), legacyFilesConfigKey("index.summary.base_url")))),
			SummaryTimeout: time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.summary.timeout_ms"), legacyFilesConfigKey("index.summary.timeout_ms")), 8000)) * time.Millisecond,
			FreshnessSLO:   time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.slo_p95_seconds"), legacyFilesConfigKey("index.slo_p95_seconds")), 30)) * time.Second,
			FileSummary: FileSummarySettings{
				Enabled:              boolFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.enabled"), legacyFilesConfigKey("index.file_summary.enabled")), true),
				Model:                strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("index.file_summary.model"), legacyFilesConfigKey("index.file_summary.model")))),
				BaseURL:              strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("index.file_summary.base_url"), legacyFilesConfigKey("index.file_summary.base_url")))),
				Timeout:              time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.timeout_ms"), legacyFilesConfigKey("index.file_summary.timeout_ms")), 20000)) * time.Millisecond,
				TargetWords:          intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.target_words"), legacyFilesConfigKey("index.file_summary.target_words")), SummaryTargetWordsDefault),
				MaxWords:             intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_words"), legacyFilesConfigKey("index.file_summary.max_words")), SummaryMaxWordsHard),
				MaxBytes:             intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_bytes"), legacyFilesConfigKey("index.file_summary.max_bytes")), SummaryMaxBytesHard),
				MaxInputTokens:       intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_input_tokens"), legacyFilesConfigKey("index.file_summary.max_input_tokens")), 16000),
				MaxReduceCalls:       intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_reduce_calls"), legacyFilesConfigKey("index.file_summary.max_reduce_calls")), 8),
				MaxTotalInputTokens:  intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_total_input_tokens"), legacyFilesConfigKey("index.file_summary.max_total_input_tokens")), 64000),
				MaxTotalOutputTokens: intFromConfig(configKeyWithFallback(ragFilesConfigKey("index.file_summary.max_total_output_tokens"), legacyFilesConfigKey("index.file_summary.max_total_output_tokens")), 4096),
				PromptVersion:        strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("index.file_summary.prompt_version"), legacyFilesConfigKey("index.file_summary.prompt_version")))),
			},
		},
		Security: SecuritySettings{
			EncryptionKEKs:        uint16StringMapFromConfig(configKeyWithFallback(ragFilesConfigKey("security.encryption_keks"), legacyFilesConfigKey("security.encryption_keks"))),
			CredentialCachePrefix: strings.TrimSpace(gconfig.S.GetString(configKeyWithFallback(ragFilesConfigKey("security.credential_cache_prefix"), legacyFilesConfigKey("security.credential_cache_prefix")))),
			CredentialCacheTTL:    time.Duration(intFromConfig(configKeyWithFallback(ragFilesConfigKey("security.credential_cache_ttl_seconds"), legacyFilesConfigKey("security.credential_cache_ttl_seconds")), 300)) * time.Second,
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
	if settings.EmbeddingBaseURL == "" {
		settings.EmbeddingBaseURL = "https://oneapi.laisky.com"
	}
	if settings.Search.LimitDefault <= 0 {
		settings.Search.LimitDefault = 20
	}
	if settings.Search.LimitMax <= 0 {
		settings.Search.LimitMax = 20
	}
	if settings.Search.LimitDefault > settings.Search.LimitMax {
		settings.Search.LimitDefault = settings.Search.LimitMax
	}
	if settings.Search.VectorCandidates <= 0 {
		settings.Search.VectorCandidates = 100
	}
	if settings.Search.LexicalCandidates <= 0 {
		settings.Search.LexicalCandidates = 100
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
		settings.Index.ChunkBytes = 500
	}
	if settings.Index.SummaryModel == "" {
		settings.Index.SummaryModel = "openai/gpt-oss-120b"
	}
	if settings.Index.SummaryBaseURL == "" {
		settings.Index.SummaryBaseURL = "https://oneapi.laisky.com"
	}
	if settings.Index.SummaryTimeout <= 0 {
		settings.Index.SummaryTimeout = 8 * time.Second
	}
	if settings.Index.FreshnessSLO <= 0 {
		settings.Index.FreshnessSLO = 30 * time.Second
	}
	applyFileSummaryDefaults(&settings.Index)
	if settings.Security.CredentialCachePrefix == "" {
		settings.Security.CredentialCachePrefix = "mcp:files:cred"
	}
	if settings.Security.CredentialCacheTTL <= 0 {
		settings.Security.CredentialCacheTTL = 300 * time.Second
	}
	if settings.Security.EncryptionKEKs == nil {
		settings.Security.EncryptionKEKs = make(map[uint16]string)
	}

	return settings
}

// applyFileSummaryDefaults fills unset file-summary knobs and clamps the hard caps.
// The model and base URL fall back to the chunk-context values so operators need not
// duplicate credentials; word and byte caps can be lowered but never raised (§3.4, §6.4).
func applyFileSummaryDefaults(index *IndexSettings) {
	fs := &index.FileSummary
	if fs.Model == "" {
		fs.Model = index.SummaryModel
	}
	if fs.BaseURL == "" {
		fs.BaseURL = index.SummaryBaseURL
	}
	if fs.Timeout <= 0 {
		fs.Timeout = 20 * time.Second
	}
	if fs.TargetWords <= 0 {
		fs.TargetWords = SummaryTargetWordsDefault
	}
	if fs.MaxWords <= 0 || fs.MaxWords > SummaryMaxWordsHard {
		fs.MaxWords = SummaryMaxWordsHard
	}
	if fs.MaxBytes <= 0 || fs.MaxBytes > SummaryMaxBytesHard {
		fs.MaxBytes = SummaryMaxBytesHard
	}
	if fs.TargetWords > fs.MaxWords {
		fs.TargetWords = fs.MaxWords
	}
	if fs.MaxInputTokens <= 0 {
		fs.MaxInputTokens = 16000
	}
	if fs.MaxReduceCalls <= 0 {
		fs.MaxReduceCalls = 8
	}
	if fs.MaxTotalInputTokens <= 0 {
		fs.MaxTotalInputTokens = 64000
	}
	if fs.MaxTotalOutputTokens <= 0 {
		fs.MaxTotalOutputTokens = 4096
	}
	if fs.PromptVersion == "" {
		fs.PromptVersion = "file_summary_v1"
	}
}

// LegacyConfigConfigured reports whether the deprecated settings.mcp.files.* tree is present.
func LegacyConfigConfigured() bool {
	return hasConfigValue(legacyFilesConfigPrefix)
}

// configKeyWithFallback returns the primary key when configured, else the fallback key.
func configKeyWithFallback(primary, fallback string) string {
	if hasConfigValue(primary) {
		return primary
	}

	return fallback
}

// hasConfigValue reports whether a config key is explicitly set.
func hasConfigValue(key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}

	return gconfig.S.Get(key) != nil
}

// ragFilesConfigKey builds a key under the new rag plugin config subtree.
func ragFilesConfigKey(suffix string) string {
	if suffix == "" {
		return ragFilesConfigPrefix
	}

	return ragFilesConfigPrefix + "." + suffix
}

// legacyFilesConfigKey builds a key under the deprecated files config subtree.
func legacyFilesConfigKey(suffix string) string {
	if suffix == "" {
		return legacyFilesConfigPrefix
	}

	return legacyFilesConfigPrefix + "." + suffix
}

// uint16StringMapFromConfig reads map-like configuration into a uint16-string map.
func uint16StringMapFromConfig(key string) map[uint16]string {
	value := gconfig.S.Get(key)
	if value == nil {
		return make(map[uint16]string)
	}

	result := make(map[uint16]string)
	appendEntry := func(rawKey, rawValue any) {
		kekID, parseErr := parseUint16(rawKey)
		if parseErr != nil {
			return
		}

		secret, ok := rawValue.(string)
		if !ok {
			return
		}

		trimmed := strings.TrimSpace(secret)
		if trimmed == "" {
			return
		}

		result[kekID] = trimmed
	}

	switch v := value.(type) {
	case map[string]any:
		for rawKey, rawValue := range v {
			appendEntry(rawKey, rawValue)
		}
	case map[any]any:
		for rawKey, rawValue := range v {
			appendEntry(rawKey, rawValue)
		}
	case map[string]string:
		for rawKey, rawValue := range v {
			appendEntry(rawKey, rawValue)
		}
	}

	return result
}

// parseUint16 converts a map key into uint16.
func parseUint16(raw any) (uint16, error) {
	switch v := raw.(type) {
	case uint16:
		return v, nil
	case int:
		if v < 0 || v > int(^uint16(0)) {
			return 0, errOutOfRange
		}
		return uint16(v), nil
	case int64:
		if v < 0 || v > int64(^uint16(0)) {
			return 0, errOutOfRange
		}
		return uint16(v), nil
	case float64:
		iv := int64(v)
		if float64(iv) != v || iv < 0 || iv > int64(^uint16(0)) {
			return 0, errOutOfRange
		}
		return uint16(iv), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, errEmpty
		}
		parsed, err := strconv.ParseUint(trimmed, 10, 16)
		if err != nil {
			return 0, err
		}
		return uint16(parsed), nil
	default:
		return 0, errUnsupportedType
	}
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
