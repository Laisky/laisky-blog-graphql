package eval

import (
	"bytes"
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
var allSuites = []string{evalRetrieval, evalRagas, "public", "ops", evalRedteam}

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
	var logBuffer bytes.Buffer
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

	auth := files.AuthContext{APIKey: evalEvalHarness, APIKeyHash: evalEvalHarness, UserIdentity: "user:eval-harness"}

	if _, ok := suiteSet[evalRetrieval]; ok {
		path := filepath.Join(cfg.GoldenDir, "memory-bench-internal-v1.jsonl")
		queries, err := LoadRetrievalQueries(path)
		switch {
		case errors.Is(err, os.ErrNotExist), isMissing(err):
			logSuiteStatus(&logBuffer, &result.Logs, evalRetrieval, evalMissing, path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: evalRetrieval, Status: evalMissing})
			result.Scorecard.RetrievalStatus = evalSkipped
		case err != nil:
			return nil, errors.Wrap(err, "load retrieval queries")
		default:
			logSuiteStatus(&logBuffer, &result.Logs, evalRetrieval, fmt.Sprintf("%d queries", len(queries)), path)
			rep, runErr := RunRetrievalEval(ctx, cfg.Plugin, queries, RetrievalOpts{Project: evalEvalHarness, Auth: auth, K: 10})
			if runErr != nil {
				return nil, errors.Wrap(runErr, "retrieval suite")
			}
			result.Scorecard.Retrieval = rep
			result.Scorecard.RetrievalStatus = "ok"
			for _, q := range rep.Queries {
				payload := map[string]any{
					metricRecallAt10: q.Recall10,
					metricNDCGAt10:   q.NDCG10,
					evalMrr:          q.MRR,
					metricHitAt5:     q.Hit5,
					"long_doc":       q.LongDoc,
					"latency_ms":     q.LatencyMS,
				}
				result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: evalRetrieval, QueryID: q.QueryID, Payload: payload})
			}
		}
	}

	if _, ok := suiteSet[evalRagas]; ok {
		path := filepath.Join(cfg.GoldenDir, "memory-bench-ragas-v1.jsonl")
		samples, err := LoadRAGASSamples(path)
		switch {
		case errors.Is(err, os.ErrNotExist), isMissing(err):
			logSuiteStatus(&logBuffer, &result.Logs, evalRagas, evalMissing, path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: evalRagas, Status: evalMissing})
			result.Scorecard.RAGAS = skippedRAGASReport()
		case err != nil:
			return nil, errors.Wrap(err, "load ragas samples")
		default:
			logSuiteStatus(&logBuffer, &result.Logs, evalRagas, fmt.Sprintf("%d samples", len(samples)), path)
			rep, runErr := RunRAGASEval(ctx, cfg.Judge, cfg.EmbeddingClient, samples, RAGASOpts{Model: "gpt-4o-mini", MaxOutTokens: 512})
			if runErr != nil {
				return nil, errors.Wrap(runErr, "ragas suite")
			}
			result.Scorecard.RAGAS = rep
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: evalRagas, Payload: map[string]any{
				evalFaithfulness:          rep.Faithfulness.Mean,
				evalContextRecall:         rep.ContextRecall.Mean,
				evalContextPrecision:      rep.ContextPrecision.Mean,
				evalAnswerCorrectness:     rep.AnswerCorrectness.Mean,
				evalAnswerRelevancy:       rep.AnswerRelevancy.Mean,
				evalContextEntitiesRecall: rep.ContextEntitiesRecall.Mean,
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
			logSuiteStatus(&logBuffer, &result.Logs, "public", evalMissing, captured)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "public", Status: evalMissing})
		case err != nil:
			return nil, errors.Wrap(err, "load public scores")
		default:
			logSuiteStatus(&logBuffer, &result.Logs, "public", "captured", captured)
			result.Scorecard.Public = scores
		}
	}

	if _, ok := suiteSet["ops"]; ok {
		path := filepath.Join(cfg.GoldenDir, "ops_queries.jsonl")
		queries, err := loadOpsQueries(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			logSuiteStatus(&logBuffer, &result.Logs, "ops", evalMissing, path)
			result.RawPerQuery = append(result.RawPerQuery, PerQueryRecord{Suite: "ops", Status: evalMissing})
			result.Scorecard.OpsStatus = evalSkipped
		case err != nil:
			return nil, errors.Wrap(err, "load ops queries")
		default:
			logSuiteStatus(&logBuffer, &result.Logs, "ops", fmt.Sprintf("%d queries", len(queries)), path)
			rep, runErr := RunOpsProbe(ctx, cfg.Plugin, queries, 1, nil)
			if runErr != nil {
				return nil, errors.Wrap(runErr, "ops probe")
			}
			result.Scorecard.Ops = rep
			result.Scorecard.OpsStatus = "ok"
		}
	}

	if _, ok := suiteSet[evalRedteam]; ok {
		attacks := OWASPAttacks2026V1()
		logSuiteStatus(&logBuffer, &result.Logs, evalRedteam, fmt.Sprintf("%d attacks (placeholders)", len(attacks)), "")
		rep, runErr := RunPromptInjectionSuite(ctx, cfg.Plugin, attacks)
		if runErr != nil {
			return nil, errors.Wrap(runErr, "redteam suite")
		}
		result.Scorecard.Adversarial.PromptInjectionBlocked = rep.NumBlocked
		result.Scorecard.Adversarial.PromptInjectionTotal = rep.NumAttacks
		result.Scorecard.Adversarial.Status = "ok"
	}

	if err := writeRunOutputs(cfg.OutDir, result, w, &logBuffer); err != nil {
		return nil, err
	}
	return result, nil
}

func logSuiteStatus(w *bytes.Buffer, logs *[]string, suite, status, path string) {
	line := fmt.Sprintf("eval suite=%s status=%s path=%s", suite, status, path)
	*logs = append(*logs, line)
	if w != nil {
		w.WriteString(line + "\n")
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
	skip := RAGASMetricStats{Status: evalSkipped}
	return RAGASReport{
		Faithfulness:          skip,
		ContextPrecision:      skip,
		ContextRecall:         skip,
		ContextEntitiesRecall: skip,
		AnswerRelevancy:       skip,
		AnswerCorrectness:     skip,
	}
}

func loadPublicScores(path string) (_ map[string]float64, retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, errors.Wrap(f.Close(), "close file")) }()
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

// writeRunOutputs reports output failures before a run can be reported as successful.
func writeRunOutputs(outDir string, result *RunResult, w io.Writer, logBuffer *bytes.Buffer) error {
	if w != nil {
		if _, err := io.Copy(w, logBuffer); err != nil {
			return errors.Wrap(err, "write evaluation diagnostics")
		}
	}
	if outDir != "" {
		if err := writeArtifacts(outDir, result); err != nil {
			return errors.Wrap(err, "write artifacts")
		}
	}

	return nil
}
