// Package benchmark provides reproducible quantitative evaluation for MCP memory plugins.
package benchmark

import "time"

const (
	// SchemaVersion identifies the persisted report contract.
	SchemaVersion = "mcp-memory-benchmark/v1"
	// HarnessVersion identifies executable behavior independently of the report schema.
	HarnessVersion = "1.1.0"
	// ReaderPromptVersion identifies the fixed answer-generation prompt.
	ReaderPromptVersion = "reader-v1"
	// DefaultProtocolVersion is the MCP protocol version used by the HTTP client.
	DefaultProtocolVersion = "2025-06-18"
	// ReportStatusValid identifies a completed run whose cases contain no execution failures.
	ReportStatusValid = "valid"
	// ReportStatusInvalid identifies a run with one or more failed cases that must not become a baseline.
	ReportStatusInvalid = "invalid"
)

// Document is one corpus item written before evaluation.
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
	ID           string            `json:"id"`
	Text         string            `json:"query"`
	PathPrefix   string            `json:"path_prefix,omitempty"`
	GoldPaths    []string          `json:"gold_paths,omitempty"`
	GoldEvidence []string          `json:"gold_evidence,omitempty"`
	Answer       string            `json:"answer,omitempty"`
	Rubric       []string          `json:"rubric,omitempty"`
	Category     string            `json:"category,omitempty"`
	Unanswerable bool              `json:"unanswerable,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
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
	TokenPrecision *float64 `json:"token_precision,omitempty"`
	TokenRecall    *float64 `json:"token_recall,omitempty"`
	TokenF1        *float64 `json:"token_f1,omitempty"`
	RubricCoverage *float64 `json:"rubric_coverage,omitempty"`
}

// CaseResult records evidence, answer, timings, and errors for one query.
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
	SearchLatenciesMS  []float64     `json:"search_latencies_ms,omitempty"`
	IndexWaitLatencyMS float64       `json:"index_wait_latency_ms"`
	SearchAttempts     int           `json:"search_attempts"`
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

// QualitySummary aggregates successful case metrics and separately reports failed cases.
type QualitySummary struct {
	Queries                    int     `json:"queries"`
	EvaluatedQueries           int     `json:"evaluated_queries"`
	FailedQueries              int     `json:"failed_queries"`
	AnswerableQueries          int     `json:"answerable_queries"`
	UnanswerableQueries        int     `json:"unanswerable_queries"`
	RetrievalMetricsAvailable  bool    `json:"retrieval_metrics_available"`
	RecallAtK                  float64 `json:"recall_at_k"`
	PrecisionAtK               float64 `json:"precision_at_k"`
	NDCGAtK                    float64 `json:"ndcg_at_k"`
	MRR                        float64 `json:"mrr"`
	HitRateAtK                 float64 `json:"hit_rate_at_k"`
	EvidenceRecall             float64 `json:"evidence_recall"`
	AbstentionMetricsAvailable bool    `json:"abstention_metrics_available"`
	AbstentionAccuracy         float64 `json:"abstention_accuracy"`
	FalseAnswerRate            float64 `json:"false_answer_rate"`
	AnswerMetricsAvailable     bool    `json:"answer_metrics_available"`
	ExactMatch                 float64 `json:"exact_match"`
	TokenPrecision             float64 `json:"token_precision"`
	TokenRecall                float64 `json:"token_recall"`
	TokenF1                    float64 `json:"token_f1"`
	RubricMetricsAvailable     bool    `json:"rubric_metrics_available"`
	RubricCoverage             float64 `json:"rubric_coverage"`
	ErrorRate                  float64 `json:"error_rate"`
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
	DocumentsPerSecond float64      `json:"documents_per_second"`
	MeanContextTokens  float64      `json:"mean_estimated_context_tokens"`
	ReaderInputTokens  int64        `json:"reader_input_tokens,omitempty"`
	ReaderOutputTokens int64        `json:"reader_output_tokens,omitempty"`
	ReaderTotalTokens  int64        `json:"reader_total_tokens,omitempty"`
}

// RunMetadata makes scorecards replayable and comparable without persisting secret-backed service endpoints.
type RunMetadata struct {
	HarnessVersion  string    `json:"harness_version"`
	GitSHA          string    `json:"git_sha,omitempty"`
	RunID           string    `json:"run_id"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	Backend         string    `json:"backend"`
	Plugin          string    `json:"plugin"`
	Project         string    `json:"project"`
	TopK            int       `json:"top_k"`
	MinScore        float64   `json:"min_score"`
	Concurrency     int       `json:"concurrency"`
	Warmup          int       `json:"warmup"`
	Repetitions     int       `json:"repetitions"`
	Seed            int64     `json:"seed"`
	IndexTimeoutMS  int64     `json:"index_timeout_ms"`
	PollIntervalMS  int64     `json:"poll_interval_ms"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ReaderModel     string    `json:"reader_model,omitempty"`
	ReaderPrompt    string    `json:"reader_prompt_version,omitempty"`
	ConfigSHA256    string    `json:"config_sha256"`
	GoVersion       string    `json:"go_version"`
	GOOS            string    `json:"goos"`
	GOARCH          string    `json:"goarch"`
	Hostname        string    `json:"hostname,omitempty"`
}

// Report is the durable JSON result of one benchmark run.
type Report struct {
	SchemaVersion   string             `json:"schema_version"`
	Status          string             `json:"status"`
	Run             RunMetadata        `json:"run"`
	Dataset         DatasetMetadata    `json:"dataset"`
	Quality         QualitySummary     `json:"quality"`
	Operational     OperationalSummary `json:"operational"`
	Categories      []CategorySummary  `json:"categories,omitempty"`
	Cases           []CaseResult       `json:"cases"`
	Warnings        []string           `json:"warnings,omitempty"`
	ExecutionErrors []string           `json:"execution_errors,omitempty"`
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
	Backend         string
	Endpoint        string
	Plugin          string
	Project         string
	TopK            int
	MinScore        float64
	Concurrency     int
	Warmup          int
	Repetitions     int
	Seed            int64
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
	MaxEvidenceRecallDrop float64
	MaxErrorRateIncrease  float64
	MaxP95LatencyIncrease float64
	PermutationIterations int
	PermutationAlpha      float64
	Seed                  int64
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
	Unit      string  `json:"unit,omitempty"`
}

// PermutationResult records a paired sign-flip test over per-query nDCG.
type PermutationResult struct {
	Pairs       int     `json:"pairs"`
	Iterations  int     `json:"iterations"`
	MeanDelta   float64 `json:"mean_delta"`
	PValue      float64 `json:"p_value"`
	Alpha       float64 `json:"alpha"`
	Significant bool    `json:"significant"`
}

// Comparison is emitted when a baseline report is supplied.
type Comparison struct {
	Compatible  bool               `json:"compatible"`
	Passed      bool               `json:"passed"`
	Reason      string             `json:"reason,omitempty"`
	Metrics     []MetricDelta      `json:"metrics,omitempty"`
	Permutation *PermutationResult `json:"paired_ndcg_permutation,omitempty"`
}
