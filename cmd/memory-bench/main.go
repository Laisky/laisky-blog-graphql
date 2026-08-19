// Command memory-bench runs reproducible quantitative evaluations against a
// deployed MCP memory plugin through the real file_* tool surface.
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

type cliConfig struct {
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
	maxErrorIncrease   float64
	maxLatencyIncrease float64
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

var errRegressionGate = errors.New("benchmark regression gate failed")

func run() error {
	cfg := parseFlags()
	if cfg.endpoint == "" {
		return errors.New("--endpoint or MCP_ENDPOINT is required")
	}
	if cfg.datasetPath == "" {
		return errors.New("--dataset is required")
	}
	if cfg.outDir == "" {
		cfg.outDir = filepath.Join("docs", "eval", "runs", time.Now().UTC().Format("20060102T150405Z"), cfg.plugin)
	}

	dataset, err := benchmark.LoadDataset(cfg.datasetPath, cfg.datasetFormat)
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: minDuration(cfg.runTimeout, 5*time.Minute)}
	client, err := benchmark.NewMCPClient(
		cfg.endpoint,
		os.Getenv(cfg.authorizationEnv),
		cfg.protocolVersion,
		httpClient,
	)
	if err != nil {
		return err
	}

	var answerer benchmark.Answerer
	if cfg.readerModel != "" {
		reader, err := benchmark.NewResponsesAnswerer(
			cfg.readerBaseURL,
			os.Getenv(cfg.readerAPIKeyEnv),
			cfg.readerModel,
			httpClient,
		)
		if err != nil {
			return err
		}
		answerer = reader
	}

	runner, err := benchmark.NewRunner(client, answerer)
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
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = client.Close(closeCtx)
	}()

	report, err := runner.Run(ctx, dataset, benchmark.RunConfig{
		Endpoint:        cfg.endpoint,
		Plugin:          cfg.plugin,
		Project:         cfg.project,
		TopK:            cfg.topK,
		MinScore:        cfg.minScore,
		Concurrency:     cfg.concurrency,
		IndexTimeout:    cfg.indexTimeout,
		PollInterval:    cfg.pollInterval,
		Cleanup:         cfg.cleanup,
		ProtocolVersion: cfg.protocolVersion,
		ReaderModel:     cfg.readerModel,
		GitSHA:          cfg.gitSHA,
	})
	if err != nil {
		return err
	}

	var comparison *benchmark.Comparison
	if cfg.baselinePath != "" {
		baseline, err := benchmark.LoadReport(cfg.baselinePath)
		if err != nil {
			return err
		}
		comp := benchmark.CompareReports(baseline, report, benchmark.GateConfig{
			MaxRecallDrop:         cfg.maxRecallDrop,
			MaxNDCGDrop:           cfg.maxNDCGDrop,
			MaxMRRDrop:            cfg.maxMRRDrop,
			MaxHitRateDrop:        cfg.maxHitRateDrop,
			MaxErrorRateIncrease:  cfg.maxErrorIncrease,
			MaxP95LatencyIncrease: cfg.maxLatencyIncrease,
		})
		comparison = &comp
	}
	if err := benchmark.WriteArtifacts(cfg.outDir, report, comparison); err != nil {
		return err
	}

	fmt.Printf("memory benchmark complete plugin=%s dataset=%s queries=%d recall@%d=%.4f ndcg@%d=%.4f mrr=%.4f p95_ms=%.2f out=%s\n",
		report.Run.Plugin,
		report.Dataset.Name,
		report.Dataset.Queries,
		report.Run.TopK,
		report.Quality.RecallAtK,
		report.Run.TopK,
		report.Quality.NDCGAtK,
		report.Quality.MRR,
		report.Operational.SearchLatency.P95,
		cfg.outDir,
	)
	if comparison != nil {
		fmt.Printf("baseline compatible=%t passed=%t\n", comparison.Compatible, comparison.Passed)
		if !comparison.Compatible || !comparison.Passed {
			return errRegressionGate
		}
	}
	return nil
}

func parseFlags() cliConfig {
	var cfg cliConfig
	flag.StringVar(&cfg.endpoint, "endpoint", os.Getenv("MCP_ENDPOINT"), "MCP Streamable HTTP endpoint; defaults to MCP_ENDPOINT.")
	flag.StringVar(&cfg.authorizationEnv, "authorization-env", "MCP_AUTHORIZATION", "Environment variable containing the full Authorization header value.")
	flag.StringVar(&cfg.protocolVersion, "protocol-version", benchmark.DefaultProtocolVersion, "MCP protocol version.")
	flag.StringVar(&cfg.plugin, "plugin", "rag", "Memory plugin: rag or pageindex.")
	flag.StringVar(&cfg.project, "project", "", "Isolated project namespace; generated when omitted.")
	flag.StringVar(&cfg.datasetPath, "dataset", "", "Dataset path.")
	flag.StringVar(&cfg.datasetFormat, "format", "auto", "Dataset format: auto, canonical, longmemeval, or locomo.")
	flag.StringVar(&cfg.outDir, "out", "", "Output directory for report.json, scorecard.md, and cases.jsonl.")
	flag.IntVar(&cfg.topK, "top-k", 10, "Number of ranked documents to evaluate.")
	flag.Float64Var(&cfg.minScore, "min-score", 0, "Drop file_search hits below this score; required for meaningful abstention scoring.")
	flag.IntVar(&cfg.concurrency, "concurrency", 4, "Concurrent ingest and query workers.")
	flag.DurationVar(&cfg.indexTimeout, "index-timeout", 30*time.Second, "Maximum polling time for a relevant hit after ingest; zero disables polling.")
	flag.DurationVar(&cfg.pollInterval, "poll-interval", 500*time.Millisecond, "Polling interval while waiting for async indexing.")
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
	flag.Float64Var(&cfg.maxErrorIncrease, "max-error-rate-increase", 0, "Maximum absolute error-rate increase.")
	flag.Float64Var(&cfg.maxLatencyIncrease, "max-p95-latency-increase", 0.20, "Maximum relative search p95 increase (0.20 = 20%).")
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
