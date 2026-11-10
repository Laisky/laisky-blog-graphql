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

// TestValidateStartupConfigWithGetterInvalidEncryptionKEKs verifies malformed multi-KEK configuration fails validation.
func TestValidateStartupConfigWithGetterInvalidEncryptionKEKs(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"files": map[string]any{
					"security": map[string]any{
						"encryption_keks": map[string]any{
							"invalid-id": "this-key-is-longer-than-16",
							"2":          "short-key",
						},
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.mcp.files.security.encryption_keks")
}

// TestValidateStartupConfigWithGetterInvalidWebsearchEngine verifies enabled engine missing required fields fails validation.
func TestValidateStartupConfigWithGetterInvalidWebsearchEngine(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"websearch": map[string]any{
				"engines": map[string]any{
					"my_google": map[string]any{
						"type":    "google",
						"enabled": true,
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.websearch.engines.my_google.api_key")
	require.Contains(t, err.Error(), "settings.websearch.engines.my_google.cx")
}

// TestValidateStartupConfigWithGetterValidFirecrawlEngine verifies an enabled firecrawl engine passes validation.
func TestValidateStartupConfigWithGetterValidFirecrawlEngine(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"websearch": map[string]any{
				"engines": map[string]any{
					"firecrawl": map[string]any{
						"type":     "firecrawl",
						"enabled":  true,
						"priority": 1,
						"api_key":  "fc-secret",
						"limit":    10,
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.NoError(t, err)
}

// TestValidateStartupConfigWithGetterInvalidFirecrawlEngine verifies an enabled firecrawl engine missing api_key fails validation.
func TestValidateStartupConfigWithGetterInvalidFirecrawlEngine(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"websearch": map[string]any{
				"engines": map[string]any{
					"firecrawl": map[string]any{
						"type":    "firecrawl",
						"enabled": true,
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.websearch.engines.firecrawl.api_key")
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
						"encryption_keks": map[string]any{
							"1": "this-key-is-longer-than-16",
							"3": "this-is-also-a-long-encryption-key",
						},
						"credential_cache_prefix":      "mcp:files:cred",
						"credential_cache_ttl_seconds": 300,
					},
				},
			},
			"websearch": map[string]any{
				"max_retry": 2,
				"engines": map[string]any{
					"my_google": map[string]any{
						"type":     "google",
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

// TestValidateStartupConfigWithGetterValidNewRAGPluginConfig verifies the new rag plugin path is accepted.
func TestValidateStartupConfigWithGetterValidNewRAGPluginConfig(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"tools": map[string]any{
					"memory": map[string]any{
						"default_plugin": "rag",
						"plugins": map[string]any{
							"rag": map[string]any{
								"list_limit_default": 10,
								"list_limit_max":     20,
								"search": map[string]any{
									"limit_default": 5,
									"limit_max":     10,
								},
								"security": map[string]any{
									"credential_cache_prefix":      "mcp:files:cred",
									"credential_cache_ttl_seconds": 300,
								},
							},
						},
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.NoError(t, err)
}

// TestValidateStartupConfigWithGetterInvalidDefaultPlugin verifies invalid plugin defaults fail fast.
func TestValidateStartupConfigWithGetterInvalidDefaultPlugin(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"tools": map[string]any{
					"memory": map[string]any{
						"default_plugin": "bogus",
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.mcp.tools.memory.default_plugin")
}

// TestValidateStartupConfigWithGetterInvalidMemoryHeuristicBaseURL verifies domain-only heuristic base URL fails validation.
func TestValidateStartupConfigWithGetterInvalidMemoryHeuristicBaseURL(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"tools": map[string]any{
					"memory": map[string]any{
						"heuristic": map[string]any{
							"base_url": "oneapi.laisky.com",
						},
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.mcp.tools.memory.heuristic.base_url")
}

// TestValidateStartupConfigWithGetterValidMemoryHeuristicBaseURL verifies absolute heuristic base URL passes validation.
func TestValidateStartupConfigWithGetterValidMemoryHeuristicBaseURL(t *testing.T) {
	cfg := map[string]any{
		"settings": map[string]any{
			"mcp": map[string]any{
				"tools": map[string]any{
					"memory": map[string]any{
						"heuristic": map[string]any{
							"base_url": "https://oneapi.laisky.com/v1",
						},
					},
				},
			},
		},
	}

	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.NoError(t, err)
}

// TestValidateStartupConfigWithGetterValidOneAPIStore verifies an explicit
// OneAPI SSO cutover configuration passes validation.
func TestValidateStartupConfigWithGetterValidOneAPIStore(t *testing.T) {
	cfg := map[string]any{"settings": map[string]any{
		"web": map[string]any{"sso": map[string]any{"user_store": "oneapi"}},
		"db": map[string]any{"oneapi": map[string]any{
			"driver": "postgres", "dsn": "postgres://user:password@db/oneapi",
			"max_idle_conns": 5, "max_open_conns": 20,
		}},
	}}
	require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(cfg)))
}

// TestValidateStartupConfigWithGetterRejectsIncompleteOneAPIStore verifies the
// service fails closed instead of silently falling back to MongoDB.
func TestValidateStartupConfigWithGetterRejectsIncompleteOneAPIStore(t *testing.T) {
	cfg := map[string]any{"settings": map[string]any{
		"web": map[string]any{"sso": map[string]any{"user_store": "oneapi"}},
		"db": map[string]any{"oneapi": map[string]any{
			"driver": "postgres", "max_idle_conns": 30, "max_open_conns": 20,
		}},
	}}
	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), "settings.db.oneapi.dsn")
	require.Contains(t, err.Error(), "max_idle_conns must be <=")
}

// TestValidateStartupConfigRejectsInvalidTrustedProxies verifies a mistyped
// trusted proxy or header fails startup with the offending entry named.
func TestValidateStartupConfigRejectsInvalidTrustedProxies(t *testing.T) {
	cfg := map[string]any{"settings": map[string]any{"web": map[string]any{
		"trusted_proxies":   []any{"100.64.0.0/10", "nginx.internal"},
		"turnstile":         map[string]any{"risk": map[string]any{"max_tracked_accounts": "many"}},
		"client_ip_headers": []any{"X-Forwarded-For"},
	}}}
	err := validateStartupConfigWithGetter(newMapConfigGetter(cfg))
	require.Error(t, err)
	require.Contains(t, err.Error(), `"nginx.internal"`)
	require.Contains(t, err.Error(), "max_tracked_accounts must be an integer")

	cfg = map[string]any{"settings": map[string]any{"web": map[string]any{
		"client_ip_headers": []any{"X Forwarded For"},
	}}}
	require.ErrorContains(t, validateStartupConfigWithGetter(newMapConfigGetter(cfg)), "not a valid header name")
}

// TestValidateStartupConfigAcceptsTrustedProxyForms verifies the default, an
// explicit empty list and explicit networks all pass validation.
func TestValidateStartupConfigAcceptsTrustedProxyForms(t *testing.T) {
	for _, proxies := range []any{nil, []any{}, []any{"100.64.0.10", "172.17.0.0/16", "fd00::/8"}} {
		web := map[string]any{}
		if proxies != nil {
			web["trusted_proxies"] = proxies
		}
		require.NoError(t, validateStartupConfigWithGetter(newMapConfigGetter(map[string]any{"settings": map[string]any{"web": web}})))
	}
}

// TestValidateStartupConfigRejectsAmbiguousTurnstilePairs verifies that several
// sites with Turnstile secrets need a default site or a global pair, so the
// pair for an unmatched host is never ambiguous.
func TestValidateStartupConfigRejectsAmbiguousTurnstilePairs(t *testing.T) {
	site := func(secret string, isDefault bool) map[string]any {
		cfg := map[string]any{"host": "x.example", "turnstile_site_key": "k", "turnstile_secret_key": secret}
		if isDefault {
			cfg["default"] = true
		}
		return cfg
	}
	build := func(sites map[string]any, global string) configGetter {
		web := map[string]any{"sites": sites}
		if global != "" {
			web["turnstile"] = map[string]any{"secret_key": global}
		}
		return newMapConfigGetter(map[string]any{"settings": map[string]any{"web": web}})
	}

	err := validateStartupConfigWithGetter(build(map[string]any{"a": site("s1", false), "b": site("s2", false)}, ""))
	require.ErrorContains(t, err, "a, b all set turnstile_secret_key but none is default")

	require.NoError(t, validateStartupConfigWithGetter(build(map[string]any{"a": site("s1", false), "b": site("s2", true)}, "")))
	require.NoError(t, validateStartupConfigWithGetter(build(map[string]any{"a": site("s1", false), "b": site("s2", false)}, "global")))
	require.NoError(t, validateStartupConfigWithGetter(build(map[string]any{"a": site("s1", false), "mcp": map[string]any{"host": "m"}}, "")),
		"a single secret is unambiguous")
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
