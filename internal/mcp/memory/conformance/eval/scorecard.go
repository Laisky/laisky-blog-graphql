package eval

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// AdversarialReport is the §7.4 [Adversarial] block aggregate.
type AdversarialReport struct {
	PromptInjectionBlocked int     `json:"prompt_injection_blocked"`
	PromptInjectionTotal   int     `json:"prompt_injection_total"`
	CrossTenantHits        int     `json:"cross_tenant_hits"`
	SupersessionCorrect    int     `json:"supersession_correct"`
	SupersessionTotal      int     `json:"supersession_total"`
	GDPRDeleteRecallP95MS  int64   `json:"gdpr_delete_recall_ms_p95"`
	WeeklyDriftNDCG10      float64 `json:"weekly_drift_ndcg10"`
	Status                 string  `json:"status,omitempty"`
}

// Scorecard is the rendered §7.4 plugin scorecard.
type Scorecard struct {
	PluginName        string
	RunID             string
	GitSHA            string
	GoldenSetVersions map[string]string
	Retrieval         RetrievalReport
	RAGAS             RAGASReport
	Public            map[string]float64
	Ops               OpsReport
	Adversarial       AdversarialReport

	RetrievalStatus string // "ok" | "skipped"
	OpsStatus       string // "ok" | "skipped"
}

// goldenSetKeys controls deterministic ordering of golden_set_* lines.
var goldenSetKeys = []string{
	"memory-bench-internal-v1",
	"memory-bench-ragas-v1",
	"financebench-150",
	"longmemeval_s",
	"beam-1m-200",
}

// WriteMarkdown emits the §7.4 template byte-for-byte; missing/skipped values
// render as `n/a` so a partial scorecard is still readable.
func (s Scorecard) WriteMarkdown(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "plugin: %s                           run_id: %s\n", emptyToDash(s.PluginName), s.runIDLine())
	fmt.Fprintf(bw, "golden_set:        %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "memory-bench-internal-v1"))
	fmt.Fprintf(bw, "ragas_set:         %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "memory-bench-ragas-v1"))
	fmt.Fprintf(bw, "public_set_a:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "financebench-150"))
	fmt.Fprintf(bw, "public_set_b:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "longmemeval_s"))
	fmt.Fprintf(bw, "public_set_c:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "beam-1m-200"))
	fmt.Fprintln(bw)

	fmt.Fprintln(bw, "[Retrieval quality — internal]")
	fmt.Fprintf(bw, "recall@10              %s\n", retrievalCell(s, s.Retrieval.Overall.Recall10))
	fmt.Fprintf(bw, "ndcg@10                %s\n", retrievalCell(s, s.Retrieval.Overall.NDCG10))
	fmt.Fprintf(bw, "mrr                    %s\n", retrievalCell(s, s.Retrieval.Overall.MRR))
	fmt.Fprintf(bw, "hit@5                  %s\n", retrievalCell(s, s.Retrieval.Overall.Hit5))
	fmt.Fprintf(bw, "ndcg@10 (long-doc)     %s\n", retrievalCell(s, s.Retrieval.LongDoc.NDCG10))
	fmt.Fprintln(bw)

	fmt.Fprintln(bw, "[Generation quality — RAGAS v0.4]")
	fmt.Fprintf(bw, "faithfulness           %s\n", ragasCell(s.RAGAS.Faithfulness))
	fmt.Fprintf(bw, "context_recall         %s\n", ragasCell(s.RAGAS.ContextRecall))
	fmt.Fprintf(bw, "context_precision      %s\n", ragasCell(s.RAGAS.ContextPrecision))
	fmt.Fprintf(bw, "answer_correctness     %s\n", ragasCell(s.RAGAS.AnswerCorrectness))
	fmt.Fprintf(bw, "answer_relevancy       %s\n", ragasCell(s.RAGAS.AnswerRelevancy))
	fmt.Fprintf(bw, "context_entities_recall %s\n", ragasCell(s.RAGAS.ContextEntitiesRecall))
	fmt.Fprintln(bw)

	fmt.Fprintln(bw, "[Public benchmarks]")
	fmt.Fprintf(bw, "financebench-150                  %s\n", publicCell(s.Public, "financebench-150"))
	fmt.Fprintf(bw, "longmemeval_s (overall + 7 cats)  %s\n", publicCell(s.Public, "longmemeval_s"))
	fmt.Fprintf(bw, "beam-1m (200, 6 cats)             %s\n", publicCell(s.Public, "beam-1m-200"))
	fmt.Fprintln(bw)

	fmt.Fprintln(bw, "[Operational]")
	fmt.Fprintf(bw, "file_search p50/p95/p99 (ms)         %s\n", opsLatencyCell(s))
	fmt.Fprintf(bw, "file_write→searchable p95 (ms)        %s\n", "n/a")
	fmt.Fprintf(bw, "tokens_in/out per search (mean,p95)   %s\n", tokensCell(s))
	fmt.Fprintf(bw, "$ per 1K searches @ <model>           %s\n", usdCell(s))
	fmt.Fprintf(bw, "index throughput (pages/min/worker)   %s\n", "n/a")
	fmt.Fprintf(bw, "cold-p95 - warm-p95 (ms)              %s\n", "n/a")
	fmt.Fprintln(bw)

	fmt.Fprintln(bw, "[Adversarial]")
	fmt.Fprintf(bw, "prompt_injection blocked       %s\n", advFracCell(s.Adversarial.PromptInjectionBlocked, s.Adversarial.PromptInjectionTotal, 12))
	fmt.Fprintf(bw, "cross_tenant_hits              %s\n", advIntCell(s.Adversarial.CrossTenantHits, s.Adversarial.Status))
	fmt.Fprintf(bw, "supersession_correct           %s\n", advFracCell(s.Adversarial.SupersessionCorrect, s.Adversarial.SupersessionTotal, 50))
	fmt.Fprintf(bw, "gdpr_delete_recall_ms_p95      %s\n", advInt64Cell(s.Adversarial.GDPRDeleteRecallP95MS, s.Adversarial.Status))
	fmt.Fprintf(bw, "weekly_drift_ndcg10            %s\n", advDriftCell(s.Adversarial.WeeklyDriftNDCG10, s.Adversarial.Status))

	return bw.Flush()
}

func (s Scorecard) runIDLine() string {
	parts := []string{}
	if s.GitSHA != "" {
		parts = append(parts, s.GitSHA)
	}
	if s.RunID != "" {
		parts = append(parts, s.RunID)
	}
	if len(parts) == 0 {
		return "n/a"
	}
	return strings.Join(parts, ":")
}

func goldenOrPlaceholder(m map[string]string, key string) string {
	for _, k := range goldenSetKeys {
		_ = k
	}
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return key
}

func retrievalCell(s Scorecard, v float64) string {
	if s.RetrievalStatus == "skipped" || s.Retrieval.Overall.NumQueries == 0 {
		return "n/a"
	}
	return formatFloat(v)
}

func ragasCell(m RAGASMetricStats) string {
	if m.Status == "skipped" || m.N == 0 {
		return "n/a"
	}
	return formatFloat(m.Mean)
}

func publicCell(m map[string]float64, key string) string {
	if m == nil {
		return "n/a"
	}
	v, ok := m[key]
	if !ok {
		return "n/a"
	}
	return formatFloat(v)
}

func opsLatencyCell(s Scorecard) string {
	if s.OpsStatus == "skipped" || s.Ops.NumQueries == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d/%d", s.Ops.P50LatencyMS, s.Ops.P95LatencyMS, s.Ops.P99LatencyMS)
}

func tokensCell(s Scorecard) string {
	if s.OpsStatus == "skipped" || s.Ops.NumQueries == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f,n/a / %.0f,n/a", s.Ops.MeanInputTokens, s.Ops.MeanOutputTokens)
}

func usdCell(s Scorecard) string {
	if s.OpsStatus == "skipped" || s.Ops.NumQueries == 0 {
		return "n/a"
	}
	return fmt.Sprintf("$%.4f", s.Ops.UsdPer1KSearches)
}

func advFracCell(num, denom, expected int) string {
	if denom == 0 {
		return "n/a"
	}
	if expected > 0 {
		return fmt.Sprintf("%d/%d", num, expected)
	}
	return fmt.Sprintf("%d/%d", num, denom)
}

func advIntCell(v int, status string) string {
	if status == "skipped" {
		return "n/a"
	}
	return strconv.Itoa(v)
}

func advInt64Cell(v int64, status string) string {
	if status == "skipped" {
		return "n/a"
	}
	return strconv.FormatInt(v, 10)
}

func advDriftCell(v float64, status string) string {
	if status == "skipped" {
		return "n/a"
	}
	return fmt.Sprintf("%.2fpp", v)
}

func emptyToDash(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}

// ParseScorecard and its helpers (parsePluginLine, splitGoldenLine,
// splitKeyValue, assignCell, parseFloatCell, assignRetrieval, assignRagas,
// assignPublic, assignOps, assignAdversarial, parseFracCell) live in
// scorecard_parse.go.
