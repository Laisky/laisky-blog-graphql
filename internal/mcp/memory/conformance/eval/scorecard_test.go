package eval

import (
	"bytes"
	"strings"
	"testing"
)

func TestScorecardWriteContainsTemplateHeaders(t *testing.T) {
	var buf bytes.Buffer
	sc := Scorecard{
		PluginName: "rag",
		RunID:      "2026-05-06T00:00:00Z",
		GitSHA:     "abcdef0",
		GoldenSetVersions: map[string]string{
			"memory-bench-internal-v1": "memory-bench-internal-v1",
		},
		RetrievalStatus: "skipped",
		OpsStatus:       "skipped",
		RAGAS:           skippedRAGASReport(),
	}
	if err := sc.WriteMarkdown(&buf); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	out := buf.String()
	for _, marker := range []string{
		"[Retrieval quality — internal]",
		"[Generation quality — RAGAS v0.4]",
		"[Public benchmarks]",
		"[Operational]",
		"[Adversarial]",
		"plugin: rag",
		"run_id: abcdef0:2026-05-06T00:00:00Z",
	} {
		if !strings.Contains(out, marker) {
			t.Fatalf("scorecard missing %q\noutput:\n%s", marker, out)
		}
	}
	if !strings.Contains(out, "n/a") {
		t.Fatalf("expected skipped cells to render n/a, got:\n%s", out)
	}
}

func TestScorecardRoundTrip(t *testing.T) {
	original := Scorecard{
		PluginName: "rag",
		RunID:      "2026-05-06",
		GitSHA:     "abc1234",
		GoldenSetVersions: map[string]string{
			"memory-bench-internal-v1": "memory-bench-internal-v1",
		},
		Retrieval: RetrievalReport{
			Overall: RetrievalAggregate{NumQueries: 10, Recall10: 0.8, NDCG10: 0.7, MRR: 0.6, Hit5: 0.9},
			LongDoc: RetrievalAggregate{NumQueries: 5, NDCG10: 0.65},
		},
		RetrievalStatus: "ok",
		RAGAS: RAGASReport{
			Faithfulness:          RAGASMetricStats{N: 1, Mean: 0.88, P95: 0.88, Status: "ok"},
			ContextRecall:         RAGASMetricStats{N: 1, Mean: 0.82, P95: 0.82, Status: "ok"},
			ContextPrecision:      RAGASMetricStats{N: 1, Mean: 0.81, P95: 0.81, Status: "ok"},
			AnswerCorrectness:     RAGASMetricStats{N: 1, Mean: 0.78, P95: 0.78, Status: "ok"},
			AnswerRelevancy:       RAGASMetricStats{Status: "skipped"},
			ContextEntitiesRecall: RAGASMetricStats{N: 1, Mean: 0.7, P95: 0.7, Status: "ok"},
		},
		Public: map[string]float64{
			"financebench-150": 0.95,
		},
		Ops: OpsReport{
			NumQueries:   1000,
			P50LatencyMS: 120,
			P95LatencyMS: 800,
			P99LatencyMS: 1500,
		},
		OpsStatus: "ok",
		Adversarial: AdversarialReport{
			PromptInjectionBlocked: 11,
			PromptInjectionTotal:   12,
			CrossTenantHits:        0,
			SupersessionCorrect:    49,
			SupersessionTotal:      50,
			GDPRDeleteRecallP95MS:  450,
			WeeklyDriftNDCG10:      1.2,
			Status:                 "ok",
		},
	}

	var buf bytes.Buffer
	if err := original.WriteMarkdown(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	parsed, err := ParseScorecard(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.PluginName != original.PluginName {
		t.Fatalf("plugin = %q, want %q", parsed.PluginName, original.PluginName)
	}
	if parsed.GitSHA != original.GitSHA {
		t.Fatalf("git sha = %q, want %q", parsed.GitSHA, original.GitSHA)
	}
	if parsed.Retrieval.Overall.Recall10 != original.Retrieval.Overall.Recall10 {
		t.Fatalf("recall@10 = %v, want %v", parsed.Retrieval.Overall.Recall10, original.Retrieval.Overall.Recall10)
	}
	if parsed.Retrieval.LongDoc.NDCG10 != original.Retrieval.LongDoc.NDCG10 {
		t.Fatalf("longdoc ndcg = %v, want %v", parsed.Retrieval.LongDoc.NDCG10, original.Retrieval.LongDoc.NDCG10)
	}
	if parsed.RAGAS.Faithfulness.Mean != original.RAGAS.Faithfulness.Mean {
		t.Fatalf("ragas faithfulness = %v, want %v", parsed.RAGAS.Faithfulness.Mean, original.RAGAS.Faithfulness.Mean)
	}
	if parsed.RAGAS.AnswerRelevancy.Status != "skipped" {
		t.Fatalf("answer_relevancy status = %q, want skipped", parsed.RAGAS.AnswerRelevancy.Status)
	}
	if parsed.Adversarial.PromptInjectionBlocked != 11 {
		t.Fatalf("prompt_injection_blocked = %d, want 11", parsed.Adversarial.PromptInjectionBlocked)
	}
	if parsed.Adversarial.SupersessionCorrect != 49 {
		t.Fatalf("supersession_correct = %d, want 49", parsed.Adversarial.SupersessionCorrect)
	}
	if parsed.Adversarial.GDPRDeleteRecallP95MS != 450 {
		t.Fatalf("gdpr p95 = %d, want 450", parsed.Adversarial.GDPRDeleteRecallP95MS)
	}
	if parsed.Public["financebench-150"] != 0.95 {
		t.Fatalf("financebench = %v, want 0.95", parsed.Public["financebench-150"])
	}
	if parsed.Ops.P95LatencyMS != 800 {
		t.Fatalf("ops p95 = %d, want 800", parsed.Ops.P95LatencyMS)
	}
}
