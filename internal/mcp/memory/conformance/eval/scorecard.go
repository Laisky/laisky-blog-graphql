package eval

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
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
	evalFinancebench150,
	"longmemeval_s",
	"beam-1m-200",
}

// WriteMarkdown emits the §7.4 template byte-for-byte; missing/skipped values
// render as `n/a` so a partial scorecard is still readable.
func (s Scorecard) WriteMarkdown(w io.Writer) error {
	var buf []byte
	buf = fmt.Appendf(buf, "plugin: %s                           run_id: %s\n", emptyToDash(s.PluginName), s.runIDLine())
	buf = fmt.Appendf(buf, "golden_set:        %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "memory-bench-internal-v1"))
	buf = fmt.Appendf(buf, "ragas_set:         %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "memory-bench-ragas-v1"))
	buf = fmt.Appendf(buf, "public_set_a:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, evalFinancebench150))
	buf = fmt.Appendf(buf, "public_set_b:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "longmemeval_s"))
	buf = fmt.Appendf(buf, "public_set_c:      %s\n", goldenOrPlaceholder(s.GoldenSetVersions, "beam-1m-200"))
	buf = fmt.Appendln(buf)

	buf = fmt.Appendln(buf, "[Retrieval quality — internal]")
	buf = fmt.Appendf(buf, "recall@10              %s\n", retrievalCell(s, s.Retrieval.Overall.Recall10))
	buf = fmt.Appendf(buf, "ndcg@10                %s\n", retrievalCell(s, s.Retrieval.Overall.NDCG10))
	buf = fmt.Appendf(buf, "mrr                    %s\n", retrievalCell(s, s.Retrieval.Overall.MRR))
	buf = fmt.Appendf(buf, "hit@5                  %s\n", retrievalCell(s, s.Retrieval.Overall.Hit5))
	buf = fmt.Appendf(buf, "ndcg@10 (long-doc)     %s\n", retrievalCell(s, s.Retrieval.LongDoc.NDCG10))
	buf = fmt.Appendln(buf)

	buf = fmt.Appendln(buf, "[Generation quality — RAGAS v0.4]")
	buf = fmt.Appendf(buf, "faithfulness           %s\n", ragasCell(s.RAGAS.Faithfulness))
	buf = fmt.Appendf(buf, "context_recall         %s\n", ragasCell(s.RAGAS.ContextRecall))
	buf = fmt.Appendf(buf, "context_precision      %s\n", ragasCell(s.RAGAS.ContextPrecision))
	buf = fmt.Appendf(buf, "answer_correctness     %s\n", ragasCell(s.RAGAS.AnswerCorrectness))
	buf = fmt.Appendf(buf, "answer_relevancy       %s\n", ragasCell(s.RAGAS.AnswerRelevancy))
	buf = fmt.Appendf(buf, "context_entities_recall %s\n", ragasCell(s.RAGAS.ContextEntitiesRecall))
	buf = fmt.Appendln(buf)

	buf = fmt.Appendln(buf, "[Public benchmarks]")
	buf = fmt.Appendf(buf, "financebench-150                  %s\n", publicCell(s.Public, evalFinancebench150))
	buf = fmt.Appendf(buf, "longmemeval_s (overall + 7 cats)  %s\n", publicCell(s.Public, "longmemeval_s"))
	buf = fmt.Appendf(buf, "beam-1m (200, 6 cats)             %s\n", publicCell(s.Public, "beam-1m-200"))
	buf = fmt.Appendln(buf)

	buf = fmt.Appendln(buf, "[Operational]")
	buf = fmt.Appendf(buf, "file_search p50/p95/p99 (ms)         %s\n", opsLatencyCell(s))
	buf = fmt.Appendf(buf, "file_write→searchable p95 (ms)        %s\n", metricUnavailable)
	buf = fmt.Appendf(buf, "tokens_in/out per search (mean,p95)   %s\n", tokensCell(s))
	buf = fmt.Appendf(buf, "$ per 1K searches @ <model>           %s\n", usdCell(s))
	buf = fmt.Appendf(buf, "index throughput (pages/min/worker)   %s\n", metricUnavailable)
	buf = fmt.Appendf(buf, "cold-p95 - warm-p95 (ms)              %s\n", metricUnavailable)
	buf = fmt.Appendln(buf)

	buf = fmt.Appendln(buf, "[Adversarial]")
	buf = fmt.Appendf(buf, "prompt_injection blocked       %s\n", advFracCell(s.Adversarial.PromptInjectionBlocked, s.Adversarial.PromptInjectionTotal, 12))
	buf = fmt.Appendf(buf, "cross_tenant_hits              %s\n", advIntCell(s.Adversarial.CrossTenantHits, s.Adversarial.Status))
	buf = fmt.Appendf(buf, "supersession_correct           %s\n", advFracCell(s.Adversarial.SupersessionCorrect, s.Adversarial.SupersessionTotal, 50))
	buf = fmt.Appendf(buf, "gdpr_delete_recall_ms_p95      %s\n", advInt64Cell(s.Adversarial.GDPRDeleteRecallP95MS, s.Adversarial.Status))
	buf = fmt.Appendf(buf, "weekly_drift_ndcg10            %s\n", advDriftCell(s.Adversarial.WeeklyDriftNDCG10, s.Adversarial.Status))

	_, err := io.Copy(w, bytes.NewReader(buf))
	return errors.Wrap(err, "write scorecard")
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
		return metricUnavailable
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
	if s.RetrievalStatus == evalSkipped || s.Retrieval.Overall.NumQueries == 0 {
		return metricUnavailable
	}
	return formatFloat(v)
}

func ragasCell(m RAGASMetricStats) string {
	if m.Status == evalSkipped || m.N == 0 {
		return metricUnavailable
	}
	return formatFloat(m.Mean)
}

func publicCell(m map[string]float64, key string) string {
	if m == nil {
		return metricUnavailable
	}
	v, ok := m[key]
	if !ok {
		return metricUnavailable
	}
	return formatFloat(v)
}

func opsLatencyCell(s Scorecard) string {
	if s.OpsStatus == evalSkipped || s.Ops.NumQueries == 0 {
		return metricUnavailable
	}
	return fmt.Sprintf("%d/%d/%d", s.Ops.P50LatencyMS, s.Ops.P95LatencyMS, s.Ops.P99LatencyMS)
}

func tokensCell(s Scorecard) string {
	if s.OpsStatus == evalSkipped || s.Ops.NumQueries == 0 {
		return metricUnavailable
	}
	return fmt.Sprintf("%.0f,n/a / %.0f,n/a", s.Ops.MeanInputTokens, s.Ops.MeanOutputTokens)
}

func usdCell(s Scorecard) string {
	if s.OpsStatus == evalSkipped || s.Ops.NumQueries == 0 {
		return metricUnavailable
	}
	return fmt.Sprintf("$%.4f", s.Ops.UsdPer1KSearches)
}

func advFracCell(num, denom, expected int) string {
	if denom == 0 {
		return metricUnavailable
	}
	if expected > 0 {
		return fmt.Sprintf("%d/%d", num, expected)
	}
	return fmt.Sprintf("%d/%d", num, denom)
}

func advIntCell(v int, status string) string {
	if status == evalSkipped {
		return metricUnavailable
	}
	return strconv.Itoa(v)
}

func advInt64Cell(v int64, status string) string {
	if status == evalSkipped {
		return metricUnavailable
	}
	return strconv.FormatInt(v, 10)
}

func advDriftCell(v float64, status string) string {
	if status == evalSkipped {
		return metricUnavailable
	}
	return fmt.Sprintf("%.2fpp", v)
}

func emptyToDash(s string) string {
	if s == "" {
		return metricUnavailable
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
