package cmd

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
)

// configGetter retrieves raw configuration values by dotted key path.
type configGetter func(key string) any

// validateStartupConfig validates startup configuration from the shared config source.
// It returns an error when any configured value is malformed or violates constraints.
func validateStartupConfig() error {
	return validateStartupConfigWithGetter(func(key string) any {
		return gconfig.S.Get(key)
	})
}

// validateStartupConfigWithGetter validates startup configuration via a key-value getter.
// It accepts a value getter and returns nil when all configured values are valid.
func validateStartupConfigWithGetter(get configGetter) error {
	if get == nil {
		return errors.New("config getter is nil")
	}

	validationErrs := make([]string, 0)

	validateRedisConfig(get, &validationErrs)
	validateMCPToolsConfig(get, &validationErrs)
	validateRAGConfig(get, &validationErrs)
	validateUserRequestsConfig(get, &validationErrs)
	validateFileIOConfig(get, &validationErrs)
	validateMCPMemoryConfig(get, &validationErrs)
	validateWebsearchConfig(get, &validationErrs)
	validateWebSiteConfig(get, &validationErrs)
	validateOpenAIConfig(get, &validationErrs)

	if len(validationErrs) == 0 {
		return nil
	}

	return errors.Errorf("invalid configuration:\n - %s", strings.Join(validationErrs, "\n - "))
}

// validateRedisConfig validates redis-related startup configuration values.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateRedisConfig(get configGetter, errs *[]string) {
	validateOptionalIntMin(get, "settings.db.redis.db", 0, errs)
}

// validateMCPToolsConfig validates MCP tool toggles.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateMCPToolsConfig(get configGetter, errs *[]string) {
	keys := []string{
		"settings.mcp.tools.web_search.enabled",
		"settings.mcp.tools.web_fetch.enabled",
		"settings.mcp.tools.ask_user.enabled",
		"settings.mcp.tools.get_user_request.enabled",
		"settings.mcp.tools.extract_key_info.enabled",
		"settings.mcp.tools.file_io.enabled",
		"settings.mcp.tools.memory.enabled",
		"settings.mcp.tools.mcp_pipe.enabled",
	}

	for _, key := range keys {
		validateOptionalBool(get, key, errs)
	}
}

// validateMCPMemoryConfig validates MCP-native memory settings.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateMCPMemoryConfig(get configGetter, errs *[]string) {
	validateOptionalIntMin(get, "settings.mcp.memory.recent_context_items", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.recall_facts_limit", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.search_limit", 1, errs)
	validateOptionalFloatRange(get, "settings.mcp.memory.compact_threshold", 0, 1, false, false, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.l1_retention_days", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.l2_retention_days", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.compaction_min_age_hours", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.summary_refresh_interval_minutes", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.max_processed_turns", 1, errs)

	validateOptionalBool(get, "settings.mcp.memory.heuristic.enabled", errs)
	validateOptionalStringNonEmpty(get, "settings.mcp.memory.heuristic.model", errs)
	validateOptionalURL(get, "settings.mcp.memory.heuristic.base_url", errs)
	validateOptionalIntMin(get, "settings.mcp.memory.heuristic.timeout_ms", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.memory.heuristic.max_output_tokens", 1, errs)
}

// validateRAGConfig validates extract_key_info configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateRAGConfig(get configGetter, errs *[]string) {
	validateOptionalBool(get, "settings.mcp.extract_key_info.enabled", errs)
	validateOptionalIntMin(get, "settings.mcp.extract_key_info.top_k_default", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.extract_key_info.top_k_limit", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.extract_key_info.max_materials_size", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.extract_key_info.max_chunk_chars", 201, errs)
	validateOptionalFloatPositive(get, "settings.mcp.extract_key_info.semantic_weight", errs)
	validateOptionalFloatPositive(get, "settings.mcp.extract_key_info.lexical_weight", errs)
}

// validateUserRequestsConfig validates get_user_request retention configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateUserRequestsConfig(get configGetter, errs *[]string) {
	validateOptionalIntMin(get, "settings.mcp.user_requests.retention_days", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.user_requests.retention_sweep_seconds", 1, errs)
}

// validateFileIOConfig validates FileIO-related configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateFileIOConfig(get configGetter, errs *[]string) {
	validateOptionalBool(get, "settings.mcp.files.allow_root_wipe", errs)
	validateOptionalInt64Min(get, "settings.mcp.files.max_payload_bytes", 1, errs)
	validateOptionalInt64Min(get, "settings.mcp.files.max_file_bytes", 1, errs)
	validateOptionalInt64Min(get, "settings.mcp.files.max_project_bytes", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.list_limit_default", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.list_limit_max", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.lock_timeout_ms", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.delete_retention_days", 1, errs)

	validateOptionalBool(get, "settings.mcp.files.search.enabled", errs)
	validateOptionalIntMin(get, "settings.mcp.files.search.limit_default", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.search.limit_max", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.search.vector_candidates", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.search.bm25_candidates", 1, errs)
	validateOptionalFloatPositive(get, "settings.mcp.files.search.fallback.semantic_weight", errs)
	validateOptionalFloatPositive(get, "settings.mcp.files.search.fallback.lexical_weight", errs)
	validateOptionalIntMin(get, "settings.mcp.files.search.rerank.timeout_ms", 1, errs)
	validateOptionalURL(get, "settings.mcp.files.search.rerank.endpoint", errs)

	validateOptionalIntMin(get, "settings.mcp.files.index.workers", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.index.batch_size", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.index.chunk_bytes", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.index.retry_backoff_ms", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.index.slo_p95_seconds", 1, errs)
	validateOptionalIntMin(get, "settings.mcp.files.index.retry_max", 0, errs)

	validateOptionalIntMin(get, "settings.mcp.files.security.credential_cache_ttl_seconds", 1, errs)
	validateOptionalStringNonEmpty(get, "settings.mcp.files.security.credential_cache_prefix", errs)

	validateFileIOSecurityKEKs(get, errs)
	validateFileIOLimitRelations(get, errs)
}

// validateWebsearchConfig validates web search engine configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateWebsearchConfig(get configGetter, errs *[]string) {
	validateOptionalIntMin(get, "settings.websearch.max_retry", 0, errs)

	rawEngines := get("settings.websearch.engines")
	if rawEngines == nil {
		return
	}

	engines := toStringMap(rawEngines)
	if engines == nil {
		appendValidationError(errs, "settings.websearch.engines must be an object")
		return
	}

	for engineName, engineVal := range engines {
		engineCfg := toStringMap(engineVal)
		if engineCfg == nil {
			appendValidationError(errs, "settings.websearch.engines.%s must be an object", engineName)
			continue
		}

		enabled := false
		if enabledVal, ok := engineCfg["enabled"]; ok {
			parsed, parseOK := parseStrictBool(enabledVal)
			if !parseOK {
				appendValidationError(errs, "settings.websearch.engines.%s.enabled must be a boolean", engineName)
			} else {
				enabled = parsed
			}
		}

		if priorityVal, ok := engineCfg["priority"]; ok {
			if _, parseErr := parseStrictInt(priorityVal); parseErr != nil {
				appendValidationError(errs, "settings.websearch.engines.%s.priority must be an integer >= 1", engineName)
			} else if priority, _ := parseStrictInt(priorityVal); priority < 1 {
				appendValidationError(errs, "settings.websearch.engines.%s.priority must be >= 1", engineName)
			}
		}

		if !enabled {
			continue
		}

		switch engineName {
		case "google":
			validateRequiredStringInMap(errs, engineCfg, "settings.websearch.engines.google.api_key")
			validateRequiredStringInMap(errs, engineCfg, "settings.websearch.engines.google.cx")
		case "serp_google":
			validateRequiredStringInMap(errs, engineCfg, "settings.websearch.engines.serp_google.api_key")
		}
	}
}

// validateWebSiteConfig validates web prefix and site routing configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateWebSiteConfig(get configGetter, errs *[]string) {
	validateOptionalPathPrefix(get, "settings.web.url_prefix", errs)
	validateOptionalPathPrefix(get, "settings.web.public_url_prefix", errs)

	rawSites := get("settings.web.sites")
	if rawSites == nil {
		return
	}

	sites := toStringMap(rawSites)
	if sites == nil {
		appendValidationError(errs, "settings.web.sites must be an object")
		return
	}

	for siteKey, siteVal := range sites {
		siteCfg := toStringMap(siteVal)
		if siteCfg == nil {
			appendValidationError(errs, "settings.web.sites.%s must be an object", siteKey)
			continue
		}

		if hostVal, ok := siteCfg["host"]; ok {
			host, parseErr := parseStrictString(hostVal)
			if parseErr != nil || !isValidHost(host) {
				appendValidationError(errs, "settings.web.sites.%s.host must be a valid host", siteKey)
			}
		}

		if routerVal, ok := siteCfg["router"]; ok {
			router, parseErr := parseStrictString(routerVal)
			if parseErr != nil {
				appendValidationError(errs, "settings.web.sites.%s.router must be a string", siteKey)
			} else if normalized := strings.ToLower(strings.TrimSpace(router)); normalized != "mcp" && normalized != "sso" {
				appendValidationError(errs, "settings.web.sites.%s.router must be one of [mcp, sso]", siteKey)
			}
		}

		if basePathVal, ok := siteCfg["public_base_path"]; ok {
			basePath, parseErr := parseStrictString(basePathVal)
			if parseErr != nil {
				appendValidationError(errs, "settings.web.sites.%s.public_base_path must be a string path", siteKey)
			} else if !isValidBasePath(basePath) {
				appendValidationError(errs, "settings.web.sites.%s.public_base_path must be empty or start with '/'", siteKey)
			}
		}
	}
}

// validateOpenAIConfig validates OpenAI-related endpoint and model configuration.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateOpenAIConfig(get configGetter, errs *[]string) {
	validateOptionalStringNonEmpty(get, "settings.openai.embedding_model", errs)
	validateOptionalURL(get, "settings.openai.base_url", errs)
}

// validateFileIOSecurityKEKs validates encryption_keks constraints for FileIO security settings.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateFileIOSecurityKEKs(get configGetter, errs *[]string) {
	raw := get("settings.mcp.files.security.encryption_keks")
	if raw == nil {
		return
	}

	keks := toStringMap(raw)
	if keks == nil {
		appendValidationError(errs, "settings.mcp.files.security.encryption_keks must be an object")
		return
	}

	for rawID, rawSecret := range keks {
		if _, parseErr := strconv.ParseUint(strings.TrimSpace(rawID), 10, 16); parseErr != nil {
			appendValidationError(errs, "settings.mcp.files.security.encryption_keks.%s must use a uint16 key id", rawID)
			continue
		}

		secret, parseErr := parseStrictString(rawSecret)
		if parseErr != nil {
			appendValidationError(errs, "settings.mcp.files.security.encryption_keks.%s must be a string", rawID)
			continue
		}

		if len(strings.TrimSpace(secret)) <= 16 {
			appendValidationError(errs, "settings.mcp.files.security.encryption_keks.%s must be longer than 16 characters", rawID)
		}
	}
}

// validateFileIOLimitRelations validates relational constraints across FileIO limit values.
// It accepts a getter and an error collector pointer and appends validation errors.
func validateFileIOLimitRelations(get configGetter, errs *[]string) {
	listDefaultRaw := get("settings.mcp.files.list_limit_default")
	listMaxRaw := get("settings.mcp.files.list_limit_max")
	if listDefaultRaw != nil && listMaxRaw != nil {
		listDefault, defaultErr := parseStrictInt(listDefaultRaw)
		listMax, maxErr := parseStrictInt(listMaxRaw)
		if defaultErr == nil && maxErr == nil && listDefault > listMax {
			appendValidationError(errs, "settings.mcp.files.list_limit_default must be <= settings.mcp.files.list_limit_max")
		}
	}

	searchDefaultRaw := get("settings.mcp.files.search.limit_default")
	searchMaxRaw := get("settings.mcp.files.search.limit_max")
	if searchDefaultRaw != nil && searchMaxRaw != nil {
		searchDefault, defaultErr := parseStrictInt(searchDefaultRaw)
		searchMax, maxErr := parseStrictInt(searchMaxRaw)
		if defaultErr == nil && maxErr == nil && searchDefault > searchMax {
			appendValidationError(errs, "settings.mcp.files.search.limit_default must be <= settings.mcp.files.search.limit_max")
		}
	}
}

// validateOptionalBool validates an optionally configured boolean key.
// It accepts a getter, the key, and an error collector pointer and appends validation errors.
func validateOptionalBool(get configGetter, key string, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	if _, ok := parseStrictBool(raw); !ok {
		appendValidationError(errs, "%s must be a boolean", key)
	}
}

// validateOptionalIntMin validates an optionally configured integer key with a minimum constraint.
// It accepts a getter, the key, a minimum value, and an error collector pointer and appends validation errors.
func validateOptionalIntMin(get configGetter, key string, min int, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictInt(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be an integer", key)
		return
	}

	if value < min {
		appendValidationError(errs, "%s must be >= %d", key, min)
	}
}

// validateOptionalInt64Min validates an optionally configured int64 key with a minimum constraint.
// It accepts a getter, the key, a minimum value, and an error collector pointer and appends validation errors.
func validateOptionalInt64Min(get configGetter, key string, min int64, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictInt64(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be an integer", key)
		return
	}

	if value < min {
		appendValidationError(errs, "%s must be >= %d", key, min)
	}
}

// validateOptionalFloatPositive validates an optionally configured positive float key.
// It accepts a getter, the key, and an error collector pointer and appends validation errors.
func validateOptionalFloatPositive(get configGetter, key string, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictFloat(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be a float", key)
		return
	}

	if value <= 0 {
		appendValidationError(errs, "%s must be > 0", key)
	}
}

// validateOptionalFloatRange validates an optionally configured float key against a numeric range.
// It accepts a getter, range bounds, inclusivity toggles, and an error collector pointer.
func validateOptionalFloatRange(get configGetter, key string, min float64, max float64, includeMin bool, includeMax bool, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictFloat(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be a float", key)
		return
	}

	validMin := value > min
	if includeMin {
		validMin = value >= min
	}
	validMax := value < max
	if includeMax {
		validMax = value <= max
	}

	if !validMin || !validMax {
		appendValidationError(errs, "%s must be within range", key)
	}
}

// validateOptionalURL validates an optionally configured absolute URL key.
// It accepts a getter, the key, and an error collector pointer and appends validation errors.
func validateOptionalURL(get configGetter, key string, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictString(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be a string URL", key)
		return
	}

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		appendValidationError(errs, "%s must not be empty", key)
		return
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		appendValidationError(errs, "%s must be a valid absolute URL", key)
	}
}

// validateOptionalPathPrefix validates an optionally configured URL base path.
// It accepts a getter, the key, and an error collector pointer and appends validation errors.
func validateOptionalPathPrefix(get configGetter, key string, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictString(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be a string path", key)
		return
	}

	if !isValidBasePath(value) {
		appendValidationError(errs, "%s must be empty or start with '/'", key)
	}
}

// validateOptionalStringNonEmpty validates an optionally configured non-empty string key.
// It accepts a getter, the key, and an error collector pointer and appends validation errors.
func validateOptionalStringNonEmpty(get configGetter, key string, errs *[]string) {
	raw := get(key)
	if raw == nil {
		return
	}

	value, parseErr := parseStrictString(raw)
	if parseErr != nil {
		appendValidationError(errs, "%s must be a string", key)
		return
	}

	if strings.TrimSpace(value) == "" {
		appendValidationError(errs, "%s must not be empty", key)
	}
}

// validateRequiredStringInMap validates that a required map field is a non-empty string.
// It accepts an error collector pointer, a source map, and the field path label, and appends validation errors.
func validateRequiredStringInMap(errs *[]string, source map[string]any, fieldPath string) {
	parts := strings.Split(fieldPath, ".")
	key := parts[len(parts)-1]
	value, ok := source[key]
	if !ok {
		appendValidationError(errs, "%s is required", fieldPath)
		return
	}

	text, parseErr := parseStrictString(value)
	if parseErr != nil || strings.TrimSpace(text) == "" {
		appendValidationError(errs, "%s must be a non-empty string", fieldPath)
	}
}

// parseStrictBool parses a value as boolean using strict conversion rules.
// It accepts a raw value and returns the parsed boolean and whether parsing succeeded.
func parseStrictBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case float64:
		if math.Trunc(v) != v {
			return false, false
		}
		return int64(v) != 0, true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return false, false
		}
		switch strings.ToLower(trimmed) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

// parseStrictInt parses a value as a strict integer.
// It accepts a raw value and returns the parsed int and an error when parsing fails.
func parseStrictInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if math.Trunc(v) != v {
			return 0, errors.Errorf("%v is not an integer", v)
		}
		return int(v), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, errors.New("empty integer string")
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, errors.Wrap(err, "atoi")
		}
		return parsed, nil
	default:
		return 0, errors.Errorf("unsupported int type %T", value)
	}
}

// parseStrictInt64 parses a value as a strict int64.
// It accepts a raw value and returns the parsed int64 and an error when parsing fails.
func parseStrictInt64(value any) (int64, error) {
	parsed, err := parseStrictInt(value)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return int64(parsed), nil
}

// parseStrictFloat parses a value as a strict floating-point number.
// It accepts a raw value and returns the parsed float64 and an error when parsing fails.
func parseStrictFloat(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, errors.New("empty float string")
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, errors.Wrap(err, "parse float")
		}
		return parsed, nil
	default:
		return 0, errors.Errorf("unsupported float type %T", value)
	}
}

// parseStrictString parses a value as a strict string.
// It accepts a raw value and returns the parsed string and an error when parsing fails.
func parseStrictString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return "", errors.Errorf("unsupported string type %T", value)
	}
}

// isValidBasePath validates a base path used for URL prefixes.
// It accepts a path string and returns whether it is empty or starts with '/'.
func isValidBasePath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return true
	}
	return strings.HasPrefix(trimmed, "/")
}

// isValidHost validates a host string without scheme or path components.
// It accepts a host string and returns true when the host is syntactically acceptable.
func isValidHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "://") || strings.Contains(trimmed, "/") {
		return false
	}
	return true
}

// appendValidationError appends a formatted validation error to the collector.
// It accepts an error slice pointer, a format string, and format arguments, and has no return value.
func appendValidationError(errs *[]string, format string, args ...any) {
	if errs == nil {
		return
	}
	*errs = append(*errs, fmt.Sprintf(format, args...))
}
