package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// allSuites is the canonical suite list; nil/empty Suites in RunConfig means all.
var allSuites = []string{"retrieval", "ragas", "public", "ops", "redteam"}

// RunConfig parameterizes a full eval run.
type RunConfig struct {
	Plugin          mcpplugin.Plugin
	PluginName      string
	GoldenDir       string
	OutDir          string
	Judge           LLMJudge
	EmbeddingClient EmbeddingClient
	GitSHA          string
	UTCRunID        string
	Suites          []string
}

// PerQueryRecord is one row of raw_per_query.jsonl. The shape is intentionally
// loose (suite-tagged) so the existing Phase-1 baseline format stays valid as
// future plugins add suites.
type PerQueryRecord struct {
	Suite   string         `json:"suite"`
	Status  string         `json:"status,omitempty"`
	QueryID string         `json:"query_id,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// RunResult bundles the rendered scorecard and the raw per-query rows.
type RunResult struct {
	Scorecard      Scorecard
	RawPerQuery    []PerQueryRecord
	PermutationOpt *PermutationResult
	Logs           []string
}

// Run iterates the configured suites and produces a Scorecard. Missing
// datasets are reported as "missing" and translated to `n/a` cells in the
// scorecard, never a hard error.
func Run(ctx context.Context, cfg RunConfig, w io.Writer) (*RunResult, error) {
	if cfg.Plugin == nil {
		return nil, errors.New("plugin is nil")
	}
	pluginName := cfg.PluginName
	if pluginName == "" {
		pluginName = cfg.Plugin.Name()
	}

	suites := cfg.Suites
	if len(suites) == 0 {
		suites = allSuites
	}
	suiteSet := make(map[string]struct{}, len(suites))
	for _, s := range suites {
		suiteSet[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}

	result := &RunResult{
		Scorecard: Scorecard{
			PluginName:        pluginName,
			RunID:             cfg.UTCRunID,
			GitSHA:            cfg.GitSHA,
			GoldenSetVersions: map[string]string{},
			Public:            map[string]float64{},
		},
	}

	auth := files.AuthContext{APIKey: "eval-harness", APIKeyHash: "eval-harness", UserIdentity: "user:eval-harness"}

	if _, ok := suiteSet["retrieval"]; ok {
		path := filepath.Join(cfg.GoldenDir, "memory-bench-internal-v1.jsonl")
		queries, err := LoadRetrievalQueries(path)
		switch {
		case errors.Is(err, os.ErrNotExist), isMissing(err):
			logSuiteStatus(w, &result.Logs, "retrieval", "missing", path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "retrieval", Status: "missing"})
			result.Scorecard.RetrievalStatus = "skipped"
		case err != nil:
			return nil, errors.Wrap(err, "load retrieval queries")
		default:
			logSuiteStatus(w, &result.Logs, "retrieval", fmt.Sprintf("%d queries", len(queries)), path)
			rep, runErr := RunRetrievalEval(ctx, cfg.Plugin, queries, RetrievalOpts{Project: "eval-harness", Auth: auth, K: 10})
			if runErr != nil {
				return nil, errors.Wrap(runErr, "retrieval suite")
			}
			result.Scorecard.Retrieval = rep
			result.Scorecard.RetrievalStatus = "ok"
			for _, q := range rep.Queries {
				payload := map[string]any{
					"recall@10":  q.Recall10,
					"ndcg@10":    q.NDCG10,
					"mrr":        q.MRR,
					"hit@5":      q.Hit5,
					"long_doc":   q.LongDoc,
					"latency_ms": q.LatencyMS,
				}
				result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "retrieval", QueryID: q.QueryID, Payload: payload})
			}
		}
	}

	if _, ok := suiteSet["ragas"]; ok {
		path := filepath.Join(cfg.GoldenDir, "memory-bench-ragas-v1.jsonl")
		samples, err := LoadRAGASSamples(path)
		switch {
		case errors.Is(err, os.ErrNotExist), isMissing(err):
			logSuiteStatus(w, &result.Logs, "ragas", "missing", path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "ragas", Status: "missing"})
			result.Scorecard.RAGAS = skippedRAGASReport()
		case err != nil:
			return nil, errors.Wrap(err, "load ragas samples")
		default:
			logSuiteStatus(w, &result.Logs, "ragas", fmt.Sprintf("%d samples", len(samples)), path)
			rep, runErr := RunRAGASEval(ctx, cfg.Judge, cfg.EmbeddingClient, samples, RAGASOpts{Model: "gpt-4o-mini", MaxOutTokens: 512})
			if runErr != nil {
				return nil, errors.Wrap(runErr, "ragas suite")
			}
			result.Scorecard.RAGAS = rep
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "ragas", Payload: map[string]any{
				"faithfulness":            rep.Faithfulness.Mean,
				"context_recall":          rep.ContextRecall.Mean,
				"context_precision":       rep.ContextPrecision.Mean,
				"answer_correctness":      rep.AnswerCorrectness.Mean,
				"answer_relevancy":        rep.AnswerRelevancy.Mean,
				"context_entities_recall": rep.ContextEntitiesRecall.Mean,
			}})
		}
	}

	if _, ok := suiteSet["public"]; ok {
		// Public benchmark suites land via separate vendored harnesses; if no
		// captured artifact is present in GoldenDir we report missing.
		captured := filepath.Join(cfg.GoldenDir, "public_scores.json")
		scores, err := loadPublicScores(captured)
		switch {
		case errors.Is(err, os.ErrNotExist):
			logSuiteStatus(w, &result.Logs, "public", "missing", captured)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "public", Status: "missing"})
		case err != nil:
			return nil, errors.Wrap(err, "load public scores")
		default:
			logSuiteStatus(w, &result.Logs, "public", "captured", captured)
			result.Scorecard.Public = scores
		}
	}

	if _, ok := suiteSet["ops"]; ok {
		path := filepath.Join(cfg.GoldenDir, "ops_queries.jsonl")
		queries, err := loadOpsQueries(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			logSuiteStatus(w, &result.Logs, "ops", "missing", path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "ops", Status: "missing"})
			result.Scorecard.OpsStatus = "skipped"
		case err != nil:
			return nil, errors.Wrap(err, "load ops queries")
		default:
			logSuiteStatus(w, &result.Logs, "ops", fmt.Sprintf("%d queries", len(queries)), path)
			rep, runErr := RunOpsProbe(ctx, cfg.Plugin, queries, 1, nil)
			if runErr != nil {
				return nil, errors.Wrap(runErr, "ops probe")
			}
			result.Scorecard.Ops = rep
			result.Scorecard.OpsStatus = "ok"
		}
	}

	if _, ok := suiteSet["redteam"]; ok {
		attacks := OWASPAttacks2026V1()
		logSuiteStatus(w, &result.Logs, "redteam", fmt.Sprintf("%d attacks (placeholders)", len(attacks)), "")
		rep, runErr := RunPromptInjectionSuite(ctx, cfg.Plugin, attacks)
		if runErr != nil {
			return nil, errors.Wrap(runErr, "redteam suite")
		}
		result.Scorecard.Adversarial.PromptInjectionBlocked = rep.NumBlocked
		result.Scorecard.Adversarial.PromptInjectionTotal = rep.NumAttacks
		result.Scorecard.Adversarial.Status = "ok"
	}

	if cfg.OutDir != "" {
		if err := writeArtifacts(cfg.OutDir, result); err != nil {
			return nil, errors.Wrap(err, "write artifacts")
		}
	}
	return result, nil
}

func logSuiteStatus(w io.Writer, logs *[]string, suite, status, path string) {
	line := fmt.Sprintf("eval suite=%s status=%s path=%s", suite, status, path)
	*logs = append(*logs, line)
	if w != nil {
		fmt.Fprintln(w, line)
	}
}

func isMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	// some wrapped errors don't unwrap cleanly via errors.Is; inspect message.
	return strings.Contains(err.Error(), "no such file or directory")
}

func skippedRAGASReport() RAGASReport {
	skip := RAGASMetricStats{Status: "skipped"}
	return RAGASReport{
		Faithfulness:          skip,
		ContextPrecision:      skip,
		ContextRecall:         skip,
		ContextEntitiesRecall: skip,
		AnswerRelevancy:       skip,
		AnswerCorrectness:     skip,
	}
}

func loadPublicScores(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out map[string]float64
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return nil, errors.Wrap(err, "decode public scores")
	}
	return out, nil
}

func loadOpsQueries(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, errors.Wrap(err, "decode ops query")
		}
		if rec.Query != "" {
			out = append(out, rec.Query)
		}
	}
	return out, nil
}

func writeArtifacts(dir string, result *RunResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.Wrap(err, "mkdir out")
	}
	scorecardPath := filepath.Join(dir, "scorecard.md")
	f, err := os.Create(scorecardPath)
	if err != nil {
		return errors.Wrapf(err, "create %s", scorecardPath)
	}
	if err := result.Scorecard.WriteMarkdown(f); err != nil {
		_ = f.Close()
		return errors.Wrap(err, "write scorecard")
	}
	if err := f.Close(); err != nil {
		return errors.Wrap(err, "close scorecard")
	}

	rawPath := filepath.Join(dir, "raw_per_query.jsonl")
	rf, err := os.Create(rawPath)
	if err != nil {
		return errors.Wrapf(err, "create %s", rawPath)
	}
	enc := json.NewEncoder(rf)
	for _, r := range result.RawPerQuery {
		if err := enc.Encode(r); err != nil {
			_ = rf.Close()
			return errors.Wrap(err, "encode raw record")
		}
	}
	return rf.Close()
}
