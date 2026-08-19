// Package benchmark provides a reproducible, end-to-end evaluation harness for
// MCP memory plugins. It deliberately talks to the public MCP tool surface so
// measurements include plugin routing, indexing, retrieval, and transport cost.
package benchmark

import "time"

const (
	// SchemaVersion identifies the persisted report contract.
	SchemaVersion = "mcp-memory-benchmark/v1"
	// HarnessVersion identifies the executable behavior independently of the report schema.
	HarnessVersion = "1.0.0"
	// ReaderPromptVersion identifies the fixed answer-generation prompt.
	ReaderPromptVersion = "reader-v1"
	// DefaultProtocolVersion is the MCP protocol version used by the HTTP client.
	DefaultProtocolVersion = "2025-06-18"
)

// Document is one corpus item written through file_write before evaluation.
type Document struct {
	ID              string            `json:"id"`
	Path            string            `json:"path"`
	Content         string            `json:"content"`
	ContentEncoding string            `json:"content_encoding,omitempty"`
	Category        string            `json:"category,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	Timestamp       string            `json:"timestamp,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// Query is one labelled retrieval and optional answer-quality case.
type Query struct {
	ID           string   `json:"id"`
	Text         string   `json:"query"`
	PathPrefix   string   `json:"path_prefix,omitempty"`
	GoldPaths    []string `json:"gold_paths,omitempty"`
	GoldEvidence []string `json:"gold_evidence,omitempty"`
	Answer       string   `json:"answer,omitempty"`
	Category     string   `json:"category,omitempty"`
	Unanswerable bool     `json:"unanswerable,omitempty"`
}

// Dataset is the normalized representation consumed by Runner.
type Dataset struct {
	Name      string     `json:"name"`
	Version   string     `json:"version"`
	Source    string     `json:"source,omitempty"`
	SHA256    string     `json:"sha256"`
	Documents []Document `json:"documents"`
	Queries   []Query    `json:"queries"`
}

// SearchHit is the stable subset of file_search output used by the harness.
type SearchHit struct {
	Project    string  `json:"project,omitempty"`
	FilePath   string  `json:"file_path"`
	SeekStart  int64   `json:"file_seek_start_bytes"`
	SeekEnd    int64   `json:"file_seek_end_bytes"`
	IsFullFile bool    `json:"is_full_file"`
	Content    string  `json:"chunk_content"`
	Score      float64 `json:"score"`
}

// AnswerResult is returned by an optional fixed reader model.
type AnswerResult struct {
	Text         string `json:"text"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
	TotalTokens  int64  `json:"total_tokens,omitempty"`
}

// CaseMetrics contains per-query quality measurements.
type CaseMetrics struct {
	RecallAtK      float64  `json:"recall_at_k"`
	PrecisionAtK   float64  `json:"precision_at_k"`
	NDCGAtK        float64  `json:"ndcg_at_k"`
	MRR            float64  `json:"mrr"`
	HitAtK         bool     `json:"hit_at_k"`
	EvidenceRecall float64  `json:"evidence_recall"`
	AbstentionOK   *bool    `json:"abstention_ok,omitempty"`
	ExactMatch     *bool    `json:"exact_match,omitempty"`
	TokenF1        *float64 `json:"token_f1,omitempty"`
}

// CaseResult records the evidence and timings for one query.
type CaseResult struct {
	QueryID            string        `json:"query_id"`
	Category           string        `json:"category,omitempty"`
	Query              string        `json:"query"`
	GoldPaths          []string      `json:"gold_paths,omitempty"`
	GoldEvidence       []string      `json:"gold_evidence,omitempty"`
	Unanswerable       bool          `json:"unanswerable,omitempty"`
	Retrieved          []SearchHit   `json:"retrieved,omitempty"`
	Metrics            CaseMetrics   `json:"metrics"`
	SearchLatencyMS    float64       `json:"search_latency_ms"`
	IndexWaitLatencyMS float64       `json:"index_wait_latency_ms"`
	SearchAttempts     int           `json:"search_attempts"`
	ProbeLatencyMS     float64       `json:"probe_latency_ms,omitempty"`
	ProbeAttempts      int           `json:"probe_attempts,omitempty"`
	EstimatedCtxTokens int           `json:"estimated_context_tokens"`
	Answer             *AnswerResult `json:"answer,omitempty"`
	Error              string        `json:"error,omitempty"`
}

// LatencyStats summarizes a latency distribution in milliseconds.
type LatencyStats struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean_ms"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

// QualitySummary aggregates case-level quality metrics.
type QualitySummary struct {
	Queries             int     `json:"queries"`
	AnswerableQueries   int     `json:"answerable_queries"`
	UnanswerableQueries int     `json:"unanswerable_queries"`
	RecallAtK           float64 `json:"recall_at_k"`
	PrecisionAtK        float64 `json:"precision_at_k"`
	NDCGAtK             float64 `json:"ndcg_at_k"`
	MRR                 float64 `json:"mrr"`
	HitRateAtK          float64 `json:"hit_rate_at_k"`
	EvidenceRecall      float64 `json:"evidence_recall"`
	AbstentionAccuracy  float64 `json:"abstention_accuracy,omitempty"`
	ExactMatch          float64 `json:"exact_match,omitempty"`
	TokenF1             float64 `json:"token_f1,omitempty"`
	ErrorRate           float64 `json:"error_rate"`
}

// CategorySummary exposes benchmark ability slices independently.
type CategorySummary struct {
	Category string         `json:"category"`
	Quality  QualitySummary `json:"quality"`
}

// OperationalSummary captures cost and latency dimensions.
type OperationalSummary struct {
	IngestLatency      LatencyStats `json:"ingest_latency"`
	SearchLatency      LatencyStats `json:"search_latency"`
	IndexWaitLatency   LatencyStats `json:"index_wait_latency"`
	ProbeLatency       LatencyStats `json:"probe_latency"`
	DocumentsPerSecond float64      `json:"documents_per_second"`
	MeanContextTokens  float64      `json:"mean_estimated_context_tokens"`
	ReaderInputTokens  int64        `json:"reader_input_tokens,omitempty"`
	ReaderOutputTokens int64        `json:"reader_output_tokens,omitempty"`
	ReaderTotalTokens  int64        `json:"reader_total_tokens,omitempty"`
}

// RunMetadata makes scorecards replayable and comparable.
type RunMetadata struct {
	HarnessVersion  string    `json:"harness_version"`
	GitSHA          string    `json:"git_sha,omitempty"`
	RunID           string    `json:"run_id"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	Endpoint        string    `json:"endpoint"`
	Plugin          string    `json:"plugin"`
	Project         string    `json:"project"`
	TopK            int       `json:"top_k"`
	MinScore        float64   `json:"min_score"`
	Concurrency     int       `json:"concurrency"`
	IndexTimeoutMS  int64     `json:"index_timeout_ms"`
	PollIntervalMS  int64     `json:"poll_interval_ms"`
	ProtocolVersion string    `json:"protocol_version"`
	ReaderModel     string    `json:"reader_model,omitempty"`
	ReaderPrompt    string    `json:"reader_prompt_version,omitempty"`
	GoVersion       string    `json:"go_version"`
	GOOS            string    `json:"goos"`
	GOARCH          string    `json:"goarch"`
}

// Report is the durable JSON result of one benchmark run.
type Report struct {
	SchemaVersion string             `json:"schema_version"`
	Run           RunMetadata        `json:"run"`
	Dataset       DatasetMetadata    `json:"dataset"`
	Quality       QualitySummary     `json:"quality"`
	Operational   OperationalSummary `json:"operational"`
	Categories    []CategorySummary  `json:"categories,omitempty"`
	Cases         []CaseResult       `json:"cases"`
}

// DatasetMetadata avoids duplicating the full corpus in report.json.
type DatasetMetadata struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Source    string `json:"source,omitempty"`
	SHA256    string `json:"sha256"`
	Documents int    `json:"documents"`
	Queries   int    `json:"queries"`
}

// RunConfig controls one benchmark execution.
type RunConfig struct {
	Endpoint        string
	Plugin          string
	Project         string
	TopK            int
	MinScore        float64
	Concurrency     int
	IndexTimeout    time.Duration
	PollInterval    time.Duration
	Cleanup         bool
	ProtocolVersion string
	ReaderModel     string
	GitSHA          string
}

// GateConfig defines candidate-vs-baseline regression limits.
type GateConfig struct {
	MaxRecallDrop         float64
	MaxNDCGDrop           float64
	MaxMRRDrop            float64
	MaxHitRateDrop        float64
	MaxErrorRateIncrease  float64
	MaxP95LatencyIncrease float64
}

// MetricDelta describes one baseline comparison.
type MetricDelta struct {
	Name      string  `json:"name"`
	Baseline  float64 `json:"baseline"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
	Limit     float64 `json:"limit"`
	Passed    bool    `json:"passed"`
	Direction string  `json:"direction"`
}

// Comparison is emitted when a baseline report is supplied.
type Comparison struct {
	Compatible bool          `json:"compatible"`
	Passed     bool          `json:"passed"`
	Reason     string        `json:"reason,omitempty"`
	Metrics    []MetricDelta `json:"metrics,omitempty"`
}
