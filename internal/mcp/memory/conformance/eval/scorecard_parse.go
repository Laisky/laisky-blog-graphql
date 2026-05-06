package eval

import (
	"bufio"
	"io"
	"sort"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// ParseScorecard reads a scorecard markdown produced by WriteMarkdown. It
// recovers the structural fields (plugin name, run id, retrieval / ragas /
// public / ops / adversarial cells) — enough for round-trip tests, not a full
// fidelity parser. Cells holding "n/a" are mapped to zero values plus a
// "skipped" status flag where the schema supports it.
func ParseScorecard(r io.Reader) (Scorecard, error) {
	out := Scorecard{
		GoldenSetVersions: map[string]string{},
		Public:            map[string]float64{},
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	section := ""
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			section = trim
			continue
		}
		if strings.HasPrefix(trim, "plugin:") {
			parsePluginLine(trim, &out)
			continue
		}
		if before, after, ok := splitGoldenLine(trim); ok {
			out.GoldenSetVersions[before] = after
			continue
		}
		key, value := splitKeyValue(trim)
		if key == "" {
			continue
		}
		if err := assignCell(section, key, value, &out); err != nil {
			return Scorecard{}, errors.Wrapf(err, "parse cell %q", key)
		}
	}
	if err := sc.Err(); err != nil {
		return Scorecard{}, errors.Wrap(err, "scan scorecard")
	}
	return out, nil
}

func parsePluginLine(line string, out *Scorecard) {
	rest := strings.TrimPrefix(line, "plugin:")
	parts := strings.SplitN(rest, "run_id:", 2)
	if len(parts) >= 1 {
		out.PluginName = strings.TrimSpace(parts[0])
	}
	if len(parts) == 2 {
		runID := strings.TrimSpace(parts[1])
		if runID == "n/a" {
			return
		}
		bits := strings.SplitN(runID, ":", 2)
		if len(bits) == 2 {
			out.GitSHA = bits[0]
			out.RunID = bits[1]
		} else {
			out.RunID = runID
		}
	}
}

func splitGoldenLine(line string) (string, string, bool) {
	for _, prefix := range []string{"golden_set:", "ragas_set:", "public_set_a:", "public_set_b:", "public_set_c:"} {
		if strings.HasPrefix(line, prefix) {
			value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			return strings.TrimSuffix(prefix, ":"), value, true
		}
	}
	return "", "", false
}

// splitKeyValue separates the leftmost label from its right-hand cell using
// runs of whitespace. The template aligns columns with spaces, never tabs.
func splitKeyValue(line string) (string, string) {
	known := []string{
		"recall@10", "ndcg@10 (long-doc)", "ndcg@10", "mrr", "hit@5",
		"faithfulness", "context_recall", "context_precision",
		"answer_correctness", "answer_relevancy", "context_entities_recall",
		"financebench-150", "longmemeval_s (overall + 7 cats)", "beam-1m (200, 6 cats)",
		"file_search p50/p95/p99 (ms)", "file_write→searchable p95 (ms)",
		"tokens_in/out per search (mean,p95)", "$ per 1K searches @ <model>",
		"index throughput (pages/min/worker)", "cold-p95 - warm-p95 (ms)",
		"prompt_injection blocked", "cross_tenant_hits", "supersession_correct",
		"gdpr_delete_recall_ms_p95", "weekly_drift_ndcg10",
	}
	sort.SliceStable(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	for _, k := range known {
		if strings.HasPrefix(line, k) {
			value := strings.TrimSpace(strings.TrimPrefix(line, k))
			return k, value
		}
	}
	return "", ""
}

func assignCell(section, key, value string, out *Scorecard) error {
	switch section {
	case "[Retrieval quality — internal]":
		return assignRetrieval(key, value, out)
	case "[Generation quality — RAGAS v0.4]":
		return assignRagas(key, value, out)
	case "[Public benchmarks]":
		return assignPublic(key, value, out)
	case "[Operational]":
		return assignOps(key, value, out)
	case "[Adversarial]":
		return assignAdversarial(key, value, out)
	}
	return nil
}

func parseFloatCell(value string) (float64, bool) {
	if value == "" || value == "n/a" {
		return 0, false
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func assignRetrieval(key, value string, out *Scorecard) error {
	f, ok := parseFloatCell(value)
	if !ok {
		out.RetrievalStatus = "skipped"
		return nil
	}
	out.Retrieval.Overall.NumQueries = 1
	switch key {
	case "recall@10":
		out.Retrieval.Overall.Recall10 = f
	case "ndcg@10":
		out.Retrieval.Overall.NDCG10 = f
	case "mrr":
		out.Retrieval.Overall.MRR = f
	case "hit@5":
		out.Retrieval.Overall.Hit5 = f
	case "ndcg@10 (long-doc)":
		out.Retrieval.LongDoc.NDCG10 = f
		out.Retrieval.LongDoc.NumQueries = 1
	}
	return nil
}

func assignRagas(key, value string, out *Scorecard) error {
	f, ok := parseFloatCell(value)
	stats := RAGASMetricStats{Status: "skipped"}
	if ok {
		stats = RAGASMetricStats{N: 1, Mean: f, P95: f, Status: "ok"}
	}
	switch key {
	case "faithfulness":
		out.RAGAS.Faithfulness = stats
	case "context_recall":
		out.RAGAS.ContextRecall = stats
	case "context_precision":
		out.RAGAS.ContextPrecision = stats
	case "answer_correctness":
		out.RAGAS.AnswerCorrectness = stats
	case "answer_relevancy":
		out.RAGAS.AnswerRelevancy = stats
	case "context_entities_recall":
		out.RAGAS.ContextEntitiesRecall = stats
	}
	return nil
}

func assignPublic(key, value string, out *Scorecard) error {
	f, ok := parseFloatCell(value)
	if !ok {
		return nil
	}
	switch key {
	case "financebench-150":
		out.Public["financebench-150"] = f
	case "longmemeval_s (overall + 7 cats)":
		out.Public["longmemeval_s"] = f
	case "beam-1m (200, 6 cats)":
		out.Public["beam-1m-200"] = f
	}
	return nil
}

func assignOps(key, value string, out *Scorecard) error {
	if value == "n/a" {
		out.OpsStatus = "skipped"
		return nil
	}
	switch key {
	case "file_search p50/p95/p99 (ms)":
		bits := strings.Split(value, "/")
		if len(bits) == 3 {
			p50, _ := strconv.ParseInt(strings.TrimSpace(bits[0]), 10, 64)
			p95, _ := strconv.ParseInt(strings.TrimSpace(bits[1]), 10, 64)
			p99, _ := strconv.ParseInt(strings.TrimSpace(bits[2]), 10, 64)
			out.Ops.P50LatencyMS = p50
			out.Ops.P95LatencyMS = p95
			out.Ops.P99LatencyMS = p99
			out.Ops.NumQueries = 1
		}
	}
	return nil
}

func assignAdversarial(key, value string, out *Scorecard) error {
	switch key {
	case "prompt_injection blocked":
		num, denom, ok := parseFracCell(value)
		if ok {
			out.Adversarial.PromptInjectionBlocked = num
			out.Adversarial.PromptInjectionTotal = denom
		}
	case "cross_tenant_hits":
		if value == "n/a" {
			out.Adversarial.Status = "skipped"
			return nil
		}
		v, _ := strconv.Atoi(value)
		out.Adversarial.CrossTenantHits = v
	case "supersession_correct":
		num, denom, ok := parseFracCell(value)
		if ok {
			out.Adversarial.SupersessionCorrect = num
			out.Adversarial.SupersessionTotal = denom
		}
	case "gdpr_delete_recall_ms_p95":
		if value == "n/a" {
			return nil
		}
		v, _ := strconv.ParseInt(value, 10, 64)
		out.Adversarial.GDPRDeleteRecallP95MS = v
	case "weekly_drift_ndcg10":
		v := strings.TrimSuffix(value, "pp")
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		out.Adversarial.WeeklyDriftNDCG10 = f
	}
	return nil
}

func parseFracCell(value string) (int, int, bool) {
	if value == "" || value == "n/a" {
		return 0, 0, false
	}
	bits := strings.SplitN(value, "/", 2)
	if len(bits) != 2 {
		return 0, 0, false
	}
	num, err1 := strconv.Atoi(strings.TrimSpace(bits[0]))
	denom, err2 := strconv.Atoi(strings.TrimSpace(bits[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return num, denom, true
}
