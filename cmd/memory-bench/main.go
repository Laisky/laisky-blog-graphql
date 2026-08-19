// Command memory-bench runs reproducible quantitative evaluations against MCP memory plugins.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/benchmark"
)

const regressionExitCode = 3

var errRegressionGate = errors.New("benchmark regression gate failed")

type cliConfig struct {
	backend            string
	endpoint           string
	authorizationEnv   string
	protocolVersion    string
	plugin             string
	project            string
	datasetPath        string
	datasetFormat      string
	outDir             string
	topK               int
	minScore           float64
	concurrency        int
	warmup             int
	repetitions        int
	seed               int64
	indexTimeout       time.Duration
	pollInterval       time.Duration
	runTimeout         time.Duration
	cleanup            bool
	readerBaseURL      string
	readerModel        string
	readerAPIKeyEnv    string
	baselinePath       string
	gitSHA             string
	maxRecallDrop      float64
	maxNDCGDrop        float64
	maxMRRDrop         float64
	maxHitRateDrop     float64
	maxEvidenceDrop    float64
	maxErrorIncrease   float64
	maxLatencyIncrease float64
	permutationRuns    int
	permutationAlpha   float64
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "memory-bench:", err)
		if errors.Is(err, errRegressionGate) {
			os.Exit(regressionExitCode)
		}
		os.Exit(1)
	}
}

func run() error {
	cfg := parseFlags()
	if cfg.datasetPath == "" {
		return errors.New("--dataset is required")
	}
	if cfg.backend == "mcp" && cfg.endpoint == "" {
		return errors.New("--endpoint or MCP_ENDPOINT is required for the mcp backend")
	}
	if cfg.outDir == "" {
		cfg.outDir = filepath.Join("docs", "eval", "runs", time.Now().UTC().Format("20060102T150405Z"), cfg.plugin)
	}

	dataset, err := benchmark.LoadDataset(cfg.datasetPath, cfg.datasetFormat)
	if err != nil {
		return err
	}

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx := baseCtx
	cancel := func() {}
	if cfg.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(baseCtx, cfg.runTimeout)
	}
	defer cancel()

	httpClient := &http.Client{Timeout: minDuration(cfg.runTimeout, 10*time.Minute)}
	backend, err := buildBackend(ctx, cfg, httpClient)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = backend.Close(closeCtx)
	}()

	var answerer benchmark.Answerer
	if cfg.readerModel != "" {
		reader, readerErr := benchmark.NewResponsesAnswerer(
			cfg.readerBaseURL, os.Getenv(cfg.readerAPIKeyEnv), cfg.readerModel, httpClient,
		)
		if readerErr != nil {
			return readerErr
		}
		answerer = reader
	}

	runner, err := benchmark.NewRunner(backend, answerer)
	if err != nil {
		return err
	}
	report, err := runner.Run(ctx, dataset, benchmark.RunConfig{
		Backend: cfg.backend, Endpoint: cfg.endpoint, Plugin: cfg.plugin, Project: cfg.project,
		TopK: cfg.topK, MinScore: cfg.minScore, Concurrency: cfg.concurrency,
		Warmup: cfg.warmup, Repetitions: cfg.repetitions, Seed: cfg.seed,
		IndexTimeout: cfg.indexTimeout, PollInterval: cfg.pollInterval, Cleanup: cfg.cleanup,
		ProtocolVersion: cfg.protocolVersion, ReaderModel: cfg.readerModel, GitSHA: cfg.gitSHA,
	})
	if err != nil {
		return err
	}

	var comparison *benchmark.Comparison
	if cfg.baselinePath != "" {
		baseline, loadErr := benchmark.LoadReport(cfg.baselinePath)
		if loadErr != nil {
			return loadErr
		}
		result := benchmark.CompareReports(baseline, report, benchmark.GateConfig{
			MaxRecallDrop: cfg.maxRecallDrop, MaxNDCGDrop: cfg.maxNDCGDrop,
			MaxMRRDrop: cfg.maxMRRDrop, MaxHitRateDrop: cfg.maxHitRateDrop,
			MaxEvidenceRecallDrop: cfg.maxEvidenceDrop, MaxErrorRateIncrease: cfg.maxErrorIncrease,
			MaxP95LatencyIncrease: cfg.maxLatencyIncrease, PermutationIterations: cfg.permutationRuns,
			PermutationAlpha: cfg.permutationAlpha, Seed: cfg.seed,
		})
		comparison = &result
	}
	if err := benchmark.WriteArtifacts(cfg.outDir, report, comparison); err != nil {
		return err
	}

	fmt.Printf("memory benchmark complete backend=%s plugin=%s dataset=%s queries=%d recall@%d=%.4f ndcg@%d=%.4f mrr=%.4f hit@%d=%.4f abstention=%.4f p95_ms=%.3f out=%s\n",
		report.Run.Backend, report.Run.Plugin, report.Dataset.Name, report.Dataset.Queries,
		report.Run.TopK, report.Quality.RecallAtK, report.Run.TopK, report.Quality.NDCGAtK,
		report.Quality.MRR, report.Run.TopK, report.Quality.HitRateAtK,
		report.Quality.AbstentionAccuracy, report.Operational.SearchLatency.P95, cfg.outDir)
	if comparison != nil {
		fmt.Printf("baseline compatible=%t passed=%t\n", comparison.Compatible, comparison.Passed)
		if !comparison.Compatible || !comparison.Passed {
			return errRegressionGate
		}
	}
	return nil
}

func buildBackend(ctx context.Context, cfg cliConfig, httpClient *http.Client) (benchmark.Backend, error) {
	switch cfg.backend {
	case "local":
		return benchmark.NewLocalPluginBackend(ctx, cfg.plugin)
	case "mcp":
		client, err := benchmark.NewMCPClient(
			cfg.endpoint, os.Getenv(cfg.authorizationEnv), cfg.protocolVersion, httpClient,
		)
		if err != nil {
			return nil, err
		}
		backend, err := benchmark.NewMCPBackend(ctx, client, cfg.plugin)
		if err != nil {
			_ = client.Close(ctx)
			return nil, err
		}
		return backend, nil
	default:
		return nil, errors.Errorf("unsupported backend %q; use mcp or local", cfg.backend)
	}
}

func parseFlags() cliConfig {
	var cfg cliConfig
	flag.StringVar(&cfg.backend, "backend", "mcp", "Backend: mcp for a deployed server or local for deterministic in-process plugin smoke tests.")
	flag.StringVar(&cfg.endpoint, "endpoint", os.Getenv("MCP_ENDPOINT"), "MCP Streamable HTTP endpoint; defaults to MCP_ENDPOINT.")
	flag.StringVar(&cfg.authorizationEnv, "authorization-env", "MCP_AUTHORIZATION", "Environment variable containing the full Authorization header value.")
	flag.StringVar(&cfg.protocolVersion, "protocol-version", benchmark.DefaultProtocolVersion, "MCP protocol version.")
	flag.StringVar(&cfg.plugin, "plugin", "rag", "Memory plugin: rag or pageindex.")
	flag.StringVar(&cfg.project, "project", "", "Isolated project namespace; generated when omitted.")
	flag.StringVar(&cfg.datasetPath, "dataset", "", "Dataset file or BEAM scenario directory.")
	flag.StringVar(&cfg.datasetFormat, "format", "auto", "Dataset format: auto, canonical, longmemeval, locomo, memoryagentbench, or beam.")
	flag.StringVar(&cfg.outDir, "out", "", "Output directory for report.json, scorecard.md, and cases.jsonl.")
	flag.IntVar(&cfg.topK, "top-k", 10, "Number of ranked chunks and unique documents to evaluate.")
	flag.Float64Var(&cfg.minScore, "min-score", 0, "Drop file_search hits below this score.")
	flag.IntVar(&cfg.concurrency, "concurrency", 4, "Concurrent ingest and query workers.")
	flag.IntVar(&cfg.warmup, "warmup", 1, "Untimed warm-up searches per query.")
	flag.IntVar(&cfg.repetitions, "repetitions", 3, "Timed search repetitions per query.")
	flag.Int64Var(&cfg.seed, "seed", 42, "Deterministic seed recorded in metadata and used by statistical tests.")
	flag.DurationVar(&cfg.indexTimeout, "index-timeout", 30*time.Second, "Maximum wait for labelled evidence to become searchable; zero disables polling.")
	flag.DurationVar(&cfg.pollInterval, "poll-interval", 500*time.Millisecond, "Polling interval while waiting for indexing.")
	flag.DurationVar(&cfg.runTimeout, "run-timeout", 30*time.Minute, "Overall run deadline.")
	flag.BoolVar(&cfg.cleanup, "cleanup", true, "Delete benchmark documents after the run.")
	flag.StringVar(&cfg.readerBaseURL, "reader-base-url", os.Getenv("MEMORY_BENCH_READER_BASE_URL"), "OpenAI Responses-compatible base URL for optional end-to-end QA.")
	flag.StringVar(&cfg.readerModel, "reader-model", os.Getenv("MEMORY_BENCH_READER_MODEL"), "Fixed reader model; empty runs retrieval-only evaluation.")
	flag.StringVar(&cfg.readerAPIKeyEnv, "reader-api-key-env", "MEMORY_BENCH_READER_API_KEY", "Environment variable containing the reader API key.")
	flag.StringVar(&cfg.baselinePath, "baseline", "", "Optional baseline report.json for regression gating.")
	flag.StringVar(&cfg.gitSHA, "git-sha", os.Getenv("GITHUB_SHA"), "Git commit recorded in run metadata; defaults to GITHUB_SHA.")
	flag.Float64Var(&cfg.maxRecallDrop, "max-recall-drop", 0.02, "Maximum absolute Recall@k drop.")
	flag.Float64Var(&cfg.maxNDCGDrop, "max-ndcg-drop", 0.02, "Maximum absolute nDCG@k drop.")
	flag.Float64Var(&cfg.maxMRRDrop, "max-mrr-drop", 0.02, "Maximum absolute MRR drop.")
	flag.Float64Var(&cfg.maxHitRateDrop, "max-hit-rate-drop", 0.02, "Maximum absolute Hit@k rate drop.")
	flag.Float64Var(&cfg.maxEvidenceDrop, "max-evidence-recall-drop", 0.02, "Maximum absolute evidence-recall drop.")
	flag.Float64Var(&cfg.maxErrorIncrease, "max-error-rate-increase", 0, "Maximum absolute error-rate increase.")
	flag.Float64Var(&cfg.maxLatencyIncrease, "max-p95-latency-increase", 0.20, "Maximum relative search p95 increase (0.20 = 20%).")
	flag.IntVar(&cfg.permutationRuns, "permutation-iterations", 10_000, "Paired nDCG sign-flip iterations.")
	flag.Float64Var(&cfg.permutationAlpha, "permutation-alpha", 0.05, "Paired test significance threshold.")
	flag.Parse()
	return cfg
}

func minDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if left < right {
		return left
	}
	return right
}
