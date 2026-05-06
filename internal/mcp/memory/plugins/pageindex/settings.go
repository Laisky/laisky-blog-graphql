// Package pageindex implements the long-document tree-reasoning memory plugin.
// Phase-2 foundation: this file only carries the Settings struct used by the
// plugin manager and cmd/api.go; the runtime implementation (indexer, llm,
// search loop) lands in subsequent files.
package pageindex

import (
	"time"

	gconfig "github.com/Laisky/go-config/v2"

	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// IndexerSettings captures the orchestration knobs for Indexer.Index/Search loops.
type IndexerSettings struct {
	TimeoutIndex   time.Duration
	TimeoutQuery   time.Duration
	MaxConcurrency int
	Retry          RetrySettings
	Cache          CacheSettings
}

// RetrySettings controls per-LLM-call exponential backoff.
type RetrySettings struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// CacheSettings controls the local bbolt response cache.
type CacheSettings struct {
	Enabled      bool
	Path         string
	MaxSizeBytes int64
}

// LLMSettings captures the OpenAI Responses-API transport configuration.
type LLMSettings struct {
	IndexingModel string
	RetrieveModel string
	APIKey        string
	BaseURL       string
}

// AlgoSettings tunes the upstream PageIndex algorithm parameters.
type AlgoSettings struct {
	TocCheckPageNum        int
	MaxPageNumEachNode     int
	MaxTokenNumEachNode    int
	GenerateNodeSummary    bool
	GenerateDocDescription bool
}

// TreeQuerySettings caps the per-search reasoning loop.
type TreeQuerySettings struct {
	MaxSteps      int
	MaxTokens     int
	CandidateDocs int
}

// PDFSettings selects the pure-Go PDF parser implementations.
type PDFSettings struct {
	TextParser    string
	OutlineParser string
}

// Settings is the top-level pageindex_plugin configuration.
type Settings struct {
	Indexer   IndexerSettings
	LLM       LLMSettings
	Algo      AlgoSettings
	TreeQuery TreeQuerySettings
	PDF       PDFSettings
}

const settingsPrefix = "settings.mcp.memory.plugins.pageindex"

// LoadSettings reads the pageindex block from gconfig.S, applying every documented
// default exactly as written in the proposal §1.4 / §2.6 / §2.7 contract. The
// concrete read-then-default pattern is used because go-config v2 has no
// *OrDefault helpers (only Get/IsSet); see internal/mcp/files/settings.go.
func LoadSettings() Settings {
	return Settings{
		Indexer: IndexerSettings{
			TimeoutIndex:   durationOr(settingsPrefix+".indexer.timeout_index", 5*time.Minute),
			TimeoutQuery:   durationOr(settingsPrefix+".indexer.timeout_query", 60*time.Second),
			MaxConcurrency: intOr(settingsPrefix+".indexer.max_concurrency", 8),
			Retry: RetrySettings{
				MaxAttempts:    intOr(settingsPrefix+".indexer.retry.max_attempts", 10),
				InitialBackoff: durationOr(settingsPrefix+".indexer.retry.initial_backoff", 250*time.Millisecond),
				MaxBackoff:     durationOr(settingsPrefix+".indexer.retry.max_backoff", 8*time.Second),
			},
			Cache: CacheSettings{
				Enabled:      gconfig.S.GetBool(settingsPrefix + ".indexer.cache.enabled"),
				Path:         stringOr(settingsPrefix+".indexer.cache.path", "/var/lib/laisky/pageindex-cache.bbolt"),
				MaxSizeBytes: int64Or(settingsPrefix+".indexer.cache.max_size_bytes", 1<<30),
			},
		},
		LLM: LLMSettings{
			IndexingModel: stringOr(settingsPrefix+".llm.indexing_model", "gpt-5.4-mini"),
			RetrieveModel: stringOr(settingsPrefix+".llm.retrieve_model", "gpt-5.4-mini"),
			APIKey:        gconfig.S.GetString(settingsPrefix + ".llm.api_key"),
			BaseURL:       gconfig.S.GetString(settingsPrefix + ".llm.base_url"),
		},
		Algo: AlgoSettings{
			TocCheckPageNum:        intOr(settingsPrefix+".algo.toc_check_page_num", 20),
			MaxPageNumEachNode:     intOr(settingsPrefix+".algo.max_page_num_each_node", 10),
			MaxTokenNumEachNode:    intOr(settingsPrefix+".algo.max_token_num_each_node", 20000),
			GenerateNodeSummary:    boolOr(settingsPrefix+".algo.generate_node_summary", true),
			GenerateDocDescription: boolOr(settingsPrefix+".algo.generate_doc_description", true),
		},
		TreeQuery: TreeQuerySettings{
			MaxSteps:      intOr(settingsPrefix+".tree_query.max_steps", 8),
			MaxTokens:     intOr(settingsPrefix+".tree_query.max_tokens", 20000),
			CandidateDocs: intOr(settingsPrefix+".tree_query.candidate_docs", 5),
		},
		PDF: PDFSettings{
			TextParser:    stringOr(settingsPrefix+".pdf.text_parser", "pdfcpu"),
			OutlineParser: stringOr(settingsPrefix+".pdf.outline_parser", "pdfcpu"),
		},
	}
}

// Enabled reports whether the pageindex_plugin should be constructed at startup.
// Per proposal §2.7 the plugin is gated on llm.api_key being non-empty.
func (s Settings) Enabled() bool {
	return s.LLM.APIKey != ""
}

// SearchModeTreeReasoning is re-exported so the manager and the pageindex
// plugin agree on the capability identifier.
var SearchModeTreeReasoning = mcpplugin.SearchModeTreeReasoning

func intOr(key string, def int) int {
	if !gconfig.S.IsSet(key) {
		return def
	}
	return gconfig.S.GetInt(key)
}

func int64Or(key string, def int64) int64 {
	if !gconfig.S.IsSet(key) {
		return def
	}
	return gconfig.S.GetInt64(key)
}

func boolOr(key string, def bool) bool {
	if !gconfig.S.IsSet(key) {
		return def
	}
	return gconfig.S.GetBool(key)
}

func stringOr(key, def string) string {
	if !gconfig.S.IsSet(key) {
		return def
	}
	v := gconfig.S.GetString(key)
	if v == "" {
		return def
	}
	return v
}

func durationOr(key string, def time.Duration) time.Duration {
	if !gconfig.S.IsSet(key) {
		return def
	}
	v := gconfig.S.GetDuration(key)
	if v == 0 {
		return def
	}
	return v
}
