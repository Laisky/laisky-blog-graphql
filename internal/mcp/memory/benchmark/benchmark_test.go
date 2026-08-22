package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetrievalAndAnswerMetrics(t *testing.T) {
	t.Parallel()
	query := Query{
		ID: "q", GoldPaths: []string{"/a", "/b"}, GoldEvidence: []string{"Ottawa", "implemented in Go"},
		Answer: "Ottawa and Go", Rubric: []string{"Ottawa", "Go"},
	}
	hits := []SearchHit{
		{FilePath: "/a", Content: "Alice moved to Ottawa."},
		{FilePath: "/x", Content: "noise"},
		{FilePath: "/b", Content: "Project Atlas is implemented in Go."},
		{FilePath: "/a", Content: "duplicate chunk"},
	}
	answer := &AnswerResult{Text: "Ottawa and Go"}
	metrics := scoreCase(query, hits, answer, 3)
	require.InDelta(t, 1.0, metrics.RecallAtK, 1e-9)
	require.InDelta(t, 2.0/3.0, metrics.PrecisionAtK, 1e-9)
	require.InDelta(t, 1.0, metrics.MRR, 1e-9)
	require.InDelta(t, 1.0, metrics.EvidenceRecall, 1e-9)
	require.NotNil(t, metrics.ExactMatch)
	require.True(t, *metrics.ExactMatch)
	require.InDelta(t, 1.0, *metrics.TokenF1, 1e-9)
	require.InDelta(t, 1.0, *metrics.RubricCoverage, 1e-9)
}

func TestEvaluatedZeroMetricsRemainSerialized(t *testing.T) {
	t.Parallel()
	cases := []CaseResult{
		{
			QueryID: "answer", Metrics: CaseMetrics{
				ExactMatch: boolPointer(false), TokenPrecision: floatPointer(0),
				TokenRecall: floatPointer(0), TokenF1: floatPointer(0), RubricCoverage: floatPointer(0),
			},
		},
		{QueryID: "abstention", Unanswerable: true, Metrics: CaseMetrics{AbstentionOK: boolPointer(false)}},
	}
	summary := aggregateQuality(cases)
	require.True(t, summary.AnswerMetricsAvailable)
	require.True(t, summary.RubricMetricsAvailable)
	require.True(t, summary.AbstentionMetricsAvailable)
	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	for _, field := range []string{
		`"exact_match":0`, `"token_precision":0`, `"token_recall":0`, `"token_f1":0`,
		`"rubric_coverage":0`, `"abstention_accuracy":0`, `"false_answer_rate":1`,
	} {
		require.Contains(t, string(raw), field)
	}

	report := testReport()
	report.Quality = summary
	var scorecard bytes.Buffer
	require.NoError(t, WriteScorecard(&scorecard, report, nil))
	require.Contains(t, scorecard.String(), "| Exact match | 0.0000 |")
	require.Contains(t, scorecard.String(), "| Token F1 | 0.0000 |")
	require.Contains(t, scorecard.String(), "| Rubric coverage | 0.0000 |")
}

func TestReportOmitsConfiguredEndpoint(t *testing.T) {
	t.Parallel()
	backend := &staticBackend{hits: []SearchHit{{FilePath: "/fact.md", Content: "fact", Score: 1}}}
	runner, err := NewRunner(backend, nil)
	require.NoError(t, err)
	report, err := runner.Run(context.Background(), Dataset{
		Name: "endpoint-redaction", Version: "1",
		Documents: []Document{{ID: "fact", Path: "/fact.md", Content: "fact"}},
		Queries:   []Query{{ID: "q", Text: "fact", GoldPaths: []string{"/fact.md"}, GoldEvidence: []string{"fact"}}},
	}, RunConfig{
		Backend: "mcp", Endpoint: "https://user:password@private.example/mcp?token=secret#fragment",
		Plugin: "rag", TopK: 1, Concurrency: 1, Repetitions: 1, Cleanup: false,
	})
	require.NoError(t, err)
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private.example")
	require.NotContains(t, string(raw), "password")
	require.NotContains(t, string(raw), "secret")
	require.NotContains(t, string(raw), `"endpoint"`)
}

func TestBenchmarkWorkflowTracksGoDependencies(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..", "..")
	for _, name := range []string{"memory-benchmark.yml", "memory-benchmark-capture.yml", "memory-benchmark-results.yml"} {
		path := filepath.Join(root, ".github", "workflows", name)
		raw, err := os.ReadFile(path)
		require.NoError(t, err, name)
		require.Contains(t, string(raw), "'go.mod'", name)
		require.Contains(t, string(raw), "'go.sum'", name)
	}
}

func TestLoadDatasetAdapters(t *testing.T) {
	t.Parallel()
	t.Run("canonical", func(t *testing.T) {
		t.Parallel()
		path := writeTempDataset(t, `{"type":"dataset","name":"c","version":"1"}
{"type":"document","id":"d","path":"/d.md","content":"hello"}
{"type":"query","id":"q","query":"hello?","gold_paths":["/d.md"],"answer":"hello"}
`)
		dataset, err := LoadDataset(path, "canonical")
		require.NoError(t, err)
		require.Equal(t, "c", dataset.Name)
		require.Len(t, dataset.Documents, 1)
		require.Len(t, dataset.Queries, 1)
		require.Len(t, dataset.SHA256, 64)
	})

	t.Run("longmemeval", func(t *testing.T) {
		t.Parallel()
		path := writeTempDataset(t, `[{"question_id":"q1","question_type":"knowledge-update","question":"What color?","answer":"blue","haystack_session_ids":["s1"],"haystack_dates":["2026-01-01"],"haystack_sessions":[[{"role":"user","content":"The color is blue","has_answer":true}]],"answer_session_ids":["s1"]}]`)
		dataset, err := LoadDataset(path, "longmemeval")
		require.NoError(t, err)
		require.Len(t, dataset.Documents, 1)
		require.Equal(t, dataset.Documents[0].Path, dataset.Queries[0].GoldPaths[0])
		require.Contains(t, dataset.Queries[0].GoldEvidence[0], "blue")
	})

	t.Run("locomo", func(t *testing.T) {
		t.Parallel()
		path := writeTempDataset(t, `[{"sample_id":"s","conversation":{"session_1":[{"speaker":"A","dia_id":"d1","text":"Lives in Ottawa"}],"session_1_date_time":"2026-01-01"},"qa":[{"question":"Where?","answer":"Ottawa","category":1,"evidence":["d1"]}]}]`)
		dataset, err := LoadDataset(path, "locomo")
		require.NoError(t, err)
		require.Len(t, dataset.Documents, 1)
		require.Len(t, dataset.Queries[0].GoldPaths, 1)
		require.Equal(t, "Lives in Ottawa", dataset.Queries[0].GoldEvidence[0])
	})

	t.Run("memoryagentbench", func(t *testing.T) {
		t.Parallel()
		path := writeTempDataset(t, `[{"context":"fact one","questions":["What fact?"],"answers":["one"],"metadata":{"source":"event_qa","question_types":["accurate-retrieval"],"qa_pair_ids":["pair-1"]}}]`)
		dataset, err := LoadDataset(path, "memoryagentbench")
		require.NoError(t, err)
		require.Equal(t, "pair-1", dataset.Queries[0].ID)
		require.Equal(t, "accurate-retrieval", dataset.Queries[0].Category)
	})

	t.Run("beam", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "probing_questions"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "chat.json"), []byte(`[{"batch_number":1,"turns":[[{"role":"user","id":7,"content":"The launch code is amber."}]]}]`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "probing_questions", "probing_questions.json"), []byte(`{"accurate_retrieval":[{"question":"What is the code?","answer":"amber","source_chat_ids":[7],"rubric":["amber"]}]}`), 0o600))
		dataset, err := LoadDataset(dir, "beam")
		require.NoError(t, err)
		require.Len(t, dataset.Documents, 1)
		require.Len(t, dataset.Queries[0].GoldPaths, 1)
		require.Equal(t, "The launch code is amber.", dataset.Queries[0].GoldEvidence[0])
	})
}

func TestMCPClientJSONTransport(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		method, _ := request["method"].(string)
		if method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(mcpSessionHeader, "session-1")
		id := request["id"]
		if method == "initialize" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": DefaultProtocolVersion}})
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{
				"structuredContent": map[string]any{"chunks": []map[string]any{{"file_path": "/a.md", "chunk_content": "hello", "score": 1.0}}},
				"content":           []map[string]any{{"type": "text", "text": "fallback"}}, "isError": false,
			},
		})
	}))
	defer server.Close()

	client, err := NewMCPClient(server.URL, "Bearer test", DefaultProtocolVersion, server.Client())
	require.NoError(t, err)
	payload, err := client.CallTool(context.Background(), "file_search", map[string]any{"query": "hello"})
	require.NoError(t, err)
	var decoded struct {
		Chunks []SearchHit `json:"chunks"`
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, "/a.md", decoded.Chunks[0].FilePath)
	require.Equal(t, int64(1), calls.Load())
	require.NoError(t, client.Close(context.Background()))
}

func TestResponsesAnswerer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"Ottawa","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`))
	}))
	defer server.Close()
	answerer, err := NewResponsesAnswerer(server.URL, "secret", "reader", server.Client())
	require.NoError(t, err)
	answer, err := answerer.Answer(context.Background(), Query{Text: "Where?"}, []SearchHit{{FilePath: "/a", Content: "Ottawa"}})
	require.NoError(t, err)
	require.Equal(t, "Ottawa", answer.Text)
	require.Equal(t, int64(12), answer.TotalTokens)
}

func TestCurrentMemoryPluginsLocalBenchmark(t *testing.T) {
	fixture := filepath.Join("..", "..", "..", "..", "tests", "eval", "memory_bench_smoke.jsonl")
	dataset, err := LoadDataset(fixture, "canonical")
	require.NoError(t, err)
	for _, pluginName := range []string{"rag", "pageindex"} {
		pluginName := pluginName
		t.Run(pluginName, func(t *testing.T) {
			backend, err := NewLocalPluginBackend(context.Background(), pluginName)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close(context.Background())) }()
			runner, err := NewRunner(backend, nil)
			require.NoError(t, err)
			report, err := runner.Run(context.Background(), dataset, RunConfig{
				Backend: "local", Plugin: pluginName, TopK: 5, MinScore: 0,
				Concurrency: 1, Warmup: 0, Repetitions: 1, Seed: 42,
				IndexTimeout: 5 * time.Second, PollInterval: 10 * time.Millisecond, Cleanup: true,
			})
			require.NoError(t, err)
			for _, result := range report.Cases {
				if result.Error != "" {
					t.Logf("PLUGIN_CASE_ERROR plugin=%s query=%s error=%s hits=%v", pluginName, result.QueryID, result.Error, result.Retrieved)
				}
			}
			require.Zero(t, report.Quality.ErrorRate)
			require.GreaterOrEqual(t, report.Quality.HitRateAtK, 0.50)
			require.GreaterOrEqual(t, report.Quality.RecallAtK, 0.50)
			t.Logf("LOCAL_PLUGIN_RESULT plugin=%s recall@5=%.4f precision@5=%.4f ndcg@5=%.4f mrr=%.4f hit@5=%.4f evidence=%.4f abstention=%.4f search_p50_ms=%.3f search_p95_ms=%.3f index_wait_p95_ms=%.3f",
				pluginName, report.Quality.RecallAtK, report.Quality.PrecisionAtK,
				report.Quality.NDCGAtK, report.Quality.MRR, report.Quality.HitRateAtK,
				report.Quality.EvidenceRecall, report.Quality.AbstentionAccuracy,
				report.Operational.SearchLatency.P50, report.Operational.SearchLatency.P95,
				report.Operational.IndexWaitLatency.P95)
		})
	}
}

func TestArtifactsAndRegressionGate(t *testing.T) {
	t.Parallel()
	report := testReport()
	dir := t.TempDir()
	require.NoError(t, WriteArtifacts(dir, report, nil))
	loaded, err := LoadReport(filepath.Join(dir, "report.json"))
	require.NoError(t, err)
	require.Equal(t, report.Run.ConfigSHA256, loaded.Run.ConfigSHA256)
	require.FileExists(t, filepath.Join(dir, "scorecard.md"))
	require.FileExists(t, filepath.Join(dir, "cases.jsonl"))

	candidate := testReport()
	candidate.Quality.NDCGAtK = 0.79
	candidate.Cases[0].Metrics.NDCGAtK = 0.79
	comparison := CompareReports(report, candidate, GateConfig{MaxNDCGDrop: 0.02, PermutationIterations: 100, Seed: 1})
	require.True(t, comparison.Compatible)
	require.True(t, comparison.Passed)
	candidate.Quality.NDCGAtK = 0.70
	comparison = CompareReports(report, candidate, GateConfig{MaxNDCGDrop: 0.02, PermutationIterations: 100, Seed: 1})
	require.False(t, comparison.Passed)
}

type staticBackend struct {
	hits []SearchHit
}

func (b *staticBackend) Name() string { return "static-test" }

func (b *staticBackend) Write(context.Context, string, Document) error { return nil }

func (b *staticBackend) Search(context.Context, string, Query, int) ([]SearchHit, error) {
	return append([]SearchHit(nil), b.hits...), nil
}

func (b *staticBackend) Delete(context.Context, string, string, bool) error { return nil }

func (b *staticBackend) Close(context.Context) error { return nil }

func testReport() *Report {
	return &Report{
		SchemaVersion: SchemaVersion,
		Run:           RunMetadata{Plugin: "rag", ConfigSHA256: "same", TopK: 5},
		Dataset:       DatasetMetadata{SHA256: "dataset", Queries: 1},
		Quality:       QualitySummary{RecallAtK: 0.8, NDCGAtK: 0.8, MRR: 0.8, HitRateAtK: 0.8, EvidenceRecall: 0.8},
		Operational:   OperationalSummary{SearchLatency: LatencyStats{P95: 10}},
		Cases:         []CaseResult{{QueryID: "q", Metrics: CaseMetrics{NDCGAtK: 0.8}}},
	}
}

func writeTempDataset(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataset.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func ExampleWriteScorecard() {
	report := testReport()
	var output strings.Builder
	_ = output
	fmt.Println(report.SchemaVersion)
	// Output: mcp-memory-benchmark/v1
}
