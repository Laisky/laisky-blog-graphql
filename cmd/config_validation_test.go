package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateStartupConfigWithGetterEmpty verifies empty configuration passes validation.
func TestValidateStartupConfigWithGetterEmpty(t *testing.T) {
	err := validateStartupConfigWithGetter(newMapConfigGetter(map[string]any{}))
	require.NoError(t, err)
}

// TestValidateStartupConfigWithGetterInvalidBoolean verifies invalid boolean configuration fails validation.
func TestValidateStartupConfigWithGetterInvalidBoolean(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"tools": map[string]any{
					"file_io": map[string]any{
						"enabled": "not-a-bool",
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.mcp.tools.file_io.enabled")
}

// TestValidateStartupConfigWithGetterInvalidEncryptionKey verifies configured short encryption key fails validation.
func TestValidateStartupConfigWithGetterInvalidEncryptionKey(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"files": map[string]any{
					"security": map[string]any{
						"encryption_key": "short-key",
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.mcp.files.security.encryption_key")
}

// TestValidateStartupConfigWithGetterInvalidWebsearchEngine verifies enabled engine missing required fields fails validation.
func TestValidateStartupConfigWithGetterInvalidWebsearchEngine(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"websearch": map[string]any{
				"engines": map[string]any{
					"google": map[string]any{
						"enabled": true,
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.websearch.engines.google.api_key")
	require.Contains(t, err.Error(), "settings.websearch.engines.google.cx")
}

// TestValidateStartupConfigWithGetterValidConfig verifies valid explicit configuration passes validation.
func TestValidateStartupConfigWithGetterValidConfig(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"db": map[string]any{
				"redis": map[string]any{"db": 0},
			},
			"openai": map[string]any{
				"embedding_model": "text-embedding-3-small",
				"base_url":        "https://oneapi.laisky.com/v1",
			},
			"mcp": map[string]any{
				"tools": map[string]any{
					"file_io": map[string]any{"enabled": true},
				},
				"extract_key_info": map[string]any{
					"enabled":            true,
					"top_k_default":      5,
					"top_k_limit":        20,
					"max_materials_size": 100000,
					"max_chunk_chars":    1500,
					"semantic_weight":    0.65,
					"lexical_weight":     0.35,
				},
				"user_requests": map[string]any{
					"retention_days":          30,
					"retention_sweep_seconds": 3600,
				},
				"files": map[string]any{
					"allow_root_wipe":       false,
					"max_payload_bytes":     1024,
					"max_file_bytes":        1024,
					"max_project_bytes":     4096,
					"list_limit_default":    20,
					"list_limit_max":        200,
					"lock_timeout_ms":       2000,
					"delete_retention_days": 7,
					"search": map[string]any{
						"enabled":           true,
						"limit_default":     5,
						"limit_max":         20,
						"vector_candidates": 30,
						"bm25_candidates":   30,
						"fallback": map[string]any{
							"semantic_weight": 0.65,
							"lexical_weight":  0.35,
						},
						"rerank": map[string]any{
							"endpoint":   "https://oneapi.laisky.com/v1/rerank",
							"timeout_ms": 10000,
						},
					},
					"index": map[string]any{
						"workers":          2,
						"batch_size":       32,
						"retry_max":        5,
						"retry_backoff_ms": 1000,
						"chunk_bytes":      1500,
						"slo_p95_seconds":  30,
					},
					"security": map[string]any{
						"encryption_key":               "this-key-is-longer-than-16",
						"encryption_kek_id":            1,
						"credential_cache_prefix":      "mcp:files:cred",
						"credential_cache_ttl_seconds": 300,
					},
				},
			},
			"websearch": map[string]any{
				"max_retry": 2,
				"engines": map[string]any{
					"google": map[string]any{
						"enabled":  true,
						"priority": 1,
						"api_key":  "x",
						"cx":       "y",
					},
				},
			},
			"web": map[string]any{
				"url_prefix":        "/mcp",
				"public_url_prefix": "/",
				"sites": map[string]any{
					"mcp": map[string]any{
						"host":             "mcp.laisky.com",
						"router":           "mcp",
						"public_base_path": "/",
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.NoError(t, err)
}

// newMapConfigGetter builds a dotted-path getter for nested map-based test configuration.
// It accepts a nested map and returns a getter function compatible with validateStartupConfigWithGetter.
func newMapConfigGetter(root map[string]any) configGetter {
	return func(key string) any {
		if key == "" {
			return nil
		}

		parts := strings.Split(key, ".")
		var current any = root
		for _, part := range parts {
			nextMap, ok := current.(map[string]any)
			if !ok {
				return nil
			}

			next, exists := nextMap[part]
			if !exists {
				return nil
			}
			current = next
		}

		return current
	}
}
