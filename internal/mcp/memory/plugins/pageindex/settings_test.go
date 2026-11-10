package pageindex

import (
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// TestLoadSettingsDefaults locks every default in the proposal §1.4 contract.
// If any default drifts here, plugin behavior diverges from upstream PageIndex
// without a paper trail.
func TestLoadSettingsDefaults(t *testing.T) {
	t.Parallel()

	// Snapshot any prior config under our prefix so we can restore it after the
	// test (gconfig.S is a process-global). We unset every key we care about.
	keys := []string{
		settingsPrefix + ".indexer.timeout_index",
		settingsPrefix + ".indexer.timeout_query",
		settingsPrefix + ".indexer.max_concurrency",
		settingsPrefix + ".indexer.retry.max_attempts",
		settingsPrefix + ".indexer.retry.initial_backoff",
		settingsPrefix + ".indexer.retry.max_backoff",
		settingsPrefix + ".indexer.cache.enabled",
		settingsPrefix + ".indexer.cache.path",
		settingsPrefix + ".indexer.cache.max_size_bytes",
		settingsPrefix + ".llm.indexing_model",
		settingsPrefix + ".llm.retrieve_model",
		settingsPrefix + ".llm.api_key",
		settingsPrefix + ".llm.base_url",
		settingsPrefix + ".algo.toc_check_page_num",
		settingsPrefix + ".algo.max_page_num_each_node",
		settingsPrefix + ".algo.max_token_num_each_node",
		settingsPrefix + ".algo.generate_node_summary",
		settingsPrefix + ".algo.generate_doc_description",
		settingsPrefix + ".tree_query.max_steps",
		settingsPrefix + ".tree_query.max_tokens",
		settingsPrefix + ".tree_query.candidate_docs",
		settingsPrefix + ".pdf.text_parser",
		settingsPrefix + ".pdf.outline_parser",
		settingsPrefix + ".limits.max_nodes_per_document",
		settingsPrefix + ".limits.max_llm_calls_per_document",
		settingsPrefix + ".limits.max_tokens_per_document",
		settingsPrefix + ".limits.window",
		settingsPrefix + ".limits.tenant_llm_calls_per_window",
		settingsPrefix + ".limits.tenant_tokens_per_window",
		settingsPrefix + ".limits.global_llm_calls_per_window",
		settingsPrefix + ".limits.global_tokens_per_window",
		settingsPrefix + ".limits.max_tracked_tenants",
		settingsPrefix + ".limits.global_reserve_percent",
	}
	prior := make(map[string]any, len(keys))
	for _, k := range keys {
		prior[k] = gconfig.Shared.Get(k)
		gconfig.Shared.Set(k, nil)
	}
	defer func() {
		for k, v := range prior {
			gconfig.Shared.Set(k, v)
		}
	}()

	s := LoadSettings()

	require.Equal(t, 5*time.Minute, s.Indexer.TimeoutIndex)
	require.Equal(t, 60*time.Second, s.Indexer.TimeoutQuery)
	require.Equal(t, 8, s.Indexer.MaxConcurrency)
	require.Equal(t, 3, s.Indexer.Retry.MaxAttempts)
	require.Equal(t, 250*time.Millisecond, s.Indexer.Retry.InitialBackoff)
	require.Equal(t, 8*time.Second, s.Indexer.Retry.MaxBackoff)
	require.False(t, s.Indexer.Cache.Enabled)
	require.Equal(t, "/var/lib/laisky/pageindex-cache.bbolt", s.Indexer.Cache.Path)
	require.Equal(t, int64(1<<30), s.Indexer.Cache.MaxSizeBytes)

	require.Equal(t, "gpt-5.4-mini", s.LLM.IndexingModel)
	require.Equal(t, "gpt-5.4-mini", s.LLM.RetrieveModel)
	require.Empty(t, s.LLM.APIKey)
	require.Empty(t, s.LLM.BaseURL)

	require.Equal(t, 20, s.Algo.TocCheckPageNum)
	require.Equal(t, 10, s.Algo.MaxPageNumEachNode)
	require.Equal(t, 20000, s.Algo.MaxTokenNumEachNode)
	require.True(t, s.Algo.GenerateNodeSummary)
	require.True(t, s.Algo.GenerateDocDescription)

	require.Equal(t, 8, s.TreeQuery.MaxSteps)
	require.Equal(t, 20000, s.TreeQuery.MaxTokens)
	require.Equal(t, 5, s.TreeQuery.CandidateDocs)

	require.Equal(t, "pdfcpu", s.PDF.TextParser)
	require.Equal(t, "pdfcpu", s.PDF.OutlineParser)

	require.Equal(t, LimitsSettings{
		MaxNodesPerDocument:     2000,
		MaxLLMCallsPerDocument:  256,
		MaxTokensPerDocument:    2_000_000,
		Window:                  time.Hour,
		TenantLLMCallsPerWindow: 1000,
		TenantTokensPerWindow:   4_000_000,
		GlobalLLMCallsPerWindow: 5000,
		GlobalTokensPerWindow:   20_000_000,
		MaxTrackedTenants:       10_000,
		GlobalReservePercent:    20,
	}, s.Limits)
}

// TestLoadSettingsLimitsOverridesAndFloors checks that operators can tune every
// ceiling and that a zero or negative value falls back to the default instead of
// disabling the ceiling.
func TestLoadSettingsLimitsOverridesAndFloors(t *testing.T) {
	overrides := map[string]any{
		settingsPrefix + ".limits.max_llm_calls_per_document":  32,
		settingsPrefix + ".limits.tenant_llm_calls_per_window": 0,
		settingsPrefix + ".limits.global_tokens_per_window":    -5,
		settingsPrefix + ".limits.window":                      "10m",
	}
	prior := make(map[string]any, len(overrides))
	for k, v := range overrides {
		prior[k] = gconfig.Shared.Get(k)
		gconfig.Shared.Set(k, v)
	}
	defer func() {
		for k, v := range prior {
			gconfig.Shared.Set(k, v)
		}
	}()

	limits := LoadSettings().Limits
	require.Equal(t, 32, limits.MaxLLMCallsPerDocument)
	require.Equal(t, 10*time.Minute, limits.Window)
	require.Equal(t, DefaultTenantLLMCallsPerWindow, limits.TenantLLMCallsPerWindow)
	require.Equal(t, DefaultGlobalTokensPerWindow, limits.GlobalTokensPerWindow)
}

// TestSettingsEnabledGate documents the proposal §2.7 enablement contract.
func TestSettingsEnabledGate(t *testing.T) {
	t.Parallel()

	require.False(t, Settings{}.Enabled())
	require.True(t, Settings{LLM: LLMSettings{APIKey: "sk-foo"}}.Enabled())
}

// TestSearchModeAlias keeps the manager/plugin agreement honest.
func TestSearchModeAlias(t *testing.T) {
	t.Parallel()

	require.Equal(t, mcpplugin.SearchModeTreeReasoning, SearchModeTreeReasoning)
}
