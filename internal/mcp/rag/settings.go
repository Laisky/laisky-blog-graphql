package rag

import (
	"fmt"
	"math"
	"strings"

	gconfig "github.com/Laisky/go-config/v2"
)

// Settings captures runtime configuration for the extract_key_info workflow.
type Settings struct {
	Enabled          bool
	TopKDefault      int
	TopKLimit        int
	MaxMaterialsSize int
	MaxChunkChars    int
	SemanticWeight   float64
	LexicalWeight    float64
	EmbeddingModel   string
	OpenAIBaseURL    string
}

// LoadSettingsFromConfig reads the shared configuration and returns a sanitized Settings instance.
func LoadSettingsFromConfig() Settings {
	cfg := Settings{
		Enabled:          gconfig.S.GetBool("settings.mcp.extract_key_info.enabled"),
		TopKDefault:      intFromConfig("settings.mcp.extract_key_info.top_k_default", 5),
		TopKLimit:        intFromConfig("settings.mcp.extract_key_info.top_k_limit", 20),
		MaxMaterialsSize: intFromConfig("settings.mcp.extract_key_info.max_materials_size", 10_000_000),
		MaxChunkChars:    intFromConfig("settings.mcp.extract_key_info.max_chunk_chars", 500),
		SemanticWeight:   floatFromConfig("settings.mcp.extract_key_info.semantic_weight", 0.65),
		LexicalWeight:    floatFromConfig("settings.mcp.extract_key_info.lexical_weight", 0.35),
		EmbeddingModel:   strings.TrimSpace(gconfig.S.GetString("settings.openai.embedding_model")),
		OpenAIBaseURL:    strings.TrimSpace(gconfig.S.GetString("settings.openai.base_url")),
	}

	if cfg.TopKDefault <= 0 {
		cfg.TopKDefault = 5
	}
	if cfg.TopKLimit <= 0 {
		cfg.TopKLimit = 20
	}
	if cfg.TopKDefault > cfg.TopKLimit {
		cfg.TopKDefault = cfg.TopKLimit
	}
	if cfg.MaxMaterialsSize <= 0 {
		cfg.MaxMaterialsSize = 10_000_000
	}
	if cfg.MaxChunkChars <= 200 {
		cfg.MaxChunkChars = 500
	}
	if cfg.SemanticWeight <= 0 {
		cfg.SemanticWeight = 0.65
	}
	if cfg.LexicalWeight <= 0 {
		cfg.LexicalWeight = 0.35
	}
	total := cfg.SemanticWeight + cfg.LexicalWeight
	if total == 0 {
		cfg.SemanticWeight = 0.65
		cfg.LexicalWeight = 0.35
	} else if math.Abs(total-1) > 0.01 {
		cfg.SemanticWeight /= total
		cfg.LexicalWeight /= total
	}
	if cfg.EmbeddingModel == "" {
		cfg.EmbeddingModel = "text-embedding-3-small"
	}
	if cfg.OpenAIBaseURL == "" {
		cfg.OpenAIBaseURL = "https://oneapi.laisky.com"
	}
	return cfg
}

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
