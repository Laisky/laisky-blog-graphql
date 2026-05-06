package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// GoldSpan describes a labelled page-range gold span for the long-doc subset.
type GoldSpan struct {
	DocID    string `json:"doc_id"`
	PageFrom int    `json:"page_from"`
	PageTo   int    `json:"page_to"`
}

// RetrievalQuery is one labelled tuple drawn from memory-bench-internal-v1.
type RetrievalQuery struct {
	ID         string     `json:"id"`
	Query      string     `json:"query"`
	GoldDocSet []string   `json:"gold_doc_set"`
	GoldSpans  []GoldSpan `json:"gold_spans,omitempty"`
	LongDoc    bool       `json:"long_doc,omitempty"`
}

// RetrievalOpts controls the retrieval-eval call shape.
type RetrievalOpts struct {
	Project string
	Auth    files.AuthContext
	K       int
}

// PerQueryRetrieval records one query's metric outcome.
type PerQueryRetrieval struct {
	QueryID    string  `json:"query_id"`
	Recall10   float64 `json:"recall_at_10"`
	NDCG10     float64 `json:"ndcg_at_10"`
	MRR        float64 `json:"mrr"`
	Hit5       bool    `json:"hit_at_5"`
	LongDoc    bool    `json:"long_doc"`
	LatencyMS  int64   `json:"latency_ms"`
	NumResults int     `json:"num_results"`
}

// RetrievalAggregate carries mean metrics over a set of queries.
type RetrievalAggregate struct {
	NumQueries int     `json:"n"`
	Recall10   float64 `json:"recall_at_10"`
	NDCG10     float64 `json:"ndcg_at_10"`
	MRR        float64 `json:"mrr"`
	Hit5       float64 `json:"hit_at_5"`
}

// RetrievalReport bundles overall and long-doc-subset aggregates.
type RetrievalReport struct {
	Overall RetrievalAggregate  `json:"overall"`
	LongDoc RetrievalAggregate  `json:"long_doc"`
	Queries []PerQueryRetrieval `json:"queries"`
}

// HitAtK reports whether any of the top-k retrieved docs is in gold.
func HitAtK(retrieved, gold []string, k int) bool {
	if k <= 0 || len(retrieved) == 0 || len(gold) == 0 {
		return false
	}
	g := toSet(gold)
	upper := k
	if upper > len(retrieved) {
		upper = len(retrieved)
	}
	for i := 0; i < upper; i++ {
		if _, ok := g[retrieved[i]]; ok {
			return true
		}
	}
	return false
}

// RecallAtK reports |retrieved@k ∩ gold| / |gold|.
func RecallAtK(retrieved, gold []string, k int) float64 {
	if len(gold) == 0 || k <= 0 {
		return 0
	}
	g := toSet(gold)
	upper := k
	if upper > len(retrieved) {
		upper = len(retrieved)
	}
	hit := 0
	for i := 0; i < upper; i++ {
		if _, ok := g[retrieved[i]]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(gold))
}

// MRR reports the mean reciprocal rank of the first relevant doc; 0 if none.
func MRR(retrieved, gold []string) float64 {
	if len(retrieved) == 0 || len(gold) == 0 {
		return 0
	}
	g := toSet(gold)
	for i, doc := range retrieved {
		if _, ok := g[doc]; ok {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// NDCGAtK reports normalized discounted cumulative gain at k with binary gain
// (1 if doc in gold, else 0) and standard log2(i+2) discount.
func NDCGAtK(retrieved, gold []string, k int) float64 {
	if k <= 0 || len(gold) == 0 || len(retrieved) == 0 {
		return 0
	}
	g := toSet(gold)
	upper := k
	if upper > len(retrieved) {
		upper = len(retrieved)
	}
	dcg := 0.0
	for i := 0; i < upper; i++ {
		if _, ok := g[retrieved[i]]; ok {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	idealUpper := len(gold)
	if idealUpper > k {
		idealUpper = k
	}
	idcg := 0.0
	for i := 0; i < idealUpper; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// LoadRetrievalQueries parses *.jsonl labelled tuples from disk.
func LoadRetrievalQueries(path string) ([]RetrievalQuery, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "open retrieval golden %s", path)
	}
	defer f.Close()

	var out []RetrievalQuery
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var q RetrievalQuery
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, errors.Wrapf(err, "decode retrieval query at %s:%d", path, line)
		}
		out = append(out, q)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "scan retrieval golden")
	}
	return out, nil
}

// RunRetrievalEval invokes Search per query and aggregates BEIR-style metrics.
func RunRetrievalEval(ctx context.Context, p mcpplugin.Plugin, queries []RetrievalQuery, opts RetrievalOpts) (RetrievalReport, error) {
	if p == nil {
		return RetrievalReport{}, errors.New("plugin is nil")
	}
	k := opts.K
	if k <= 0 {
		k = 10
	}
	project := opts.Project
	if project == "" {
		project = "eval-harness"
	}

	report := RetrievalReport{}
	for _, q := range queries {
		started := time.Now()
		res, err := p.Search(ctx, opts.Auth, project, q.Query, "", k)
		latency := time.Since(started).Milliseconds()
		if err != nil {
			return RetrievalReport{}, errors.Wrapf(err, "search query %s", q.ID)
		}
		retrieved := dedupePaths(res.Chunks)
		rec := PerQueryRetrieval{
			QueryID:    q.ID,
			Recall10:   RecallAtK(retrieved, q.GoldDocSet, k),
			NDCG10:     NDCGAtK(retrieved, q.GoldDocSet, k),
			MRR:        MRR(retrieved, q.GoldDocSet),
			Hit5:       HitAtK(retrieved, q.GoldDocSet, 5),
			LongDoc:    q.LongDoc,
			LatencyMS:  latency,
			NumResults: len(retrieved),
		}
		report.Queries = append(report.Queries, rec)
	}

	report.Overall = aggregate(report.Queries, func(_ PerQueryRetrieval) bool { return true })
	report.LongDoc = aggregate(report.Queries, func(r PerQueryRetrieval) bool { return r.LongDoc })
	return report, nil
}

func aggregate(records []PerQueryRetrieval, keep func(PerQueryRetrieval) bool) RetrievalAggregate {
	var agg RetrievalAggregate
	hit5Count := 0
	for _, r := range records {
		if !keep(r) {
			continue
		}
		agg.NumQueries++
		agg.Recall10 += r.Recall10
		agg.NDCG10 += r.NDCG10
		agg.MRR += r.MRR
		if r.Hit5 {
			hit5Count++
		}
	}
	if agg.NumQueries == 0 {
		return agg
	}
	n := float64(agg.NumQueries)
	agg.Recall10 /= n
	agg.NDCG10 /= n
	agg.MRR /= n
	agg.Hit5 = float64(hit5Count) / n
	return agg
}

func toSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

// dedupePaths preserves rank order and drops repeated FilePath entries so the
// computed metrics treat each gold doc as a distinct hit even when the plugin
// returns multiple chunks per file.
func dedupePaths(chunks []files.ChunkEntry) []string {
	seen := make(map[string]struct{}, len(chunks))
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if _, ok := seen[c.FilePath]; ok {
			continue
		}
		seen[c.FilePath] = struct{}{}
		out = append(out, c.FilePath)
	}
	return out
}

// SortedDocIDs returns a stable copy of gold doc IDs (helper for tests / debug).
func SortedDocIDs(in []string) []string {
	cp := append([]string(nil), in...)
	sort.Strings(cp)
	return cp
}
