package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// Runner ingests a dataset, waits for indexing, evaluates queries, and aggregates a report.
type Runner struct {
	backend  Backend
	answerer Answerer
}

// NewRunner constructs a benchmark runner.
func NewRunner(backend Backend, answerer Answerer) (*Runner, error) {
	if backend == nil {
		return nil, errors.New("benchmark backend is nil")
	}
	return &Runner{backend: backend, answerer: answerer}, nil
}

// Run executes one reproducible benchmark run.
func (r *Runner) Run(ctx context.Context, dataset Dataset, config RunConfig) (*Report, error) {
	normalizedDataset, err := normalizeDataset(dataset)
	if err != nil {
		return nil, errors.Wrap(err, "normalize benchmark dataset")
	}
	config = normalizeRunConfig(config)
	startedAt := time.Now().UTC()
	if config.Project == "" {
		prefix := normalizedDataset.SHA256
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		config.Project = fmt.Sprintf("memory-bench-%s-%d", prefix, startedAt.UnixNano())
	}

	ingestLatencies, ingestElapsed, err := r.ingest(ctx, normalizedDataset.Documents, config)
	if err != nil {
		return nil, errors.Wrap(err, "ingest benchmark corpus")
	}

	waitResults := make(map[string]readinessResult, len(normalizedDataset.Queries))
	for _, query := range normalizedDataset.Queries {
		if query.Unanswerable || config.IndexTimeout <= 0 {
			continue
		}
		waitResults[query.ID] = r.waitForRelevant(ctx, config.Project, query, config)
	}

	cases := r.evaluateQueries(ctx, normalizedDataset.Queries, waitResults, config)
	warnings := make([]string, 0)
	if config.Cleanup {
		if cleanupErr := r.cleanup(ctx, normalizedDataset.Documents, config); cleanupErr != nil {
			warnings = append(warnings, cleanupErr.Error())
		}
	}

	searchLatencies := make([]float64, 0)
	waitLatencies := make([]float64, 0, len(waitResults))
	contextTokens := 0
	readerInputTokens := int64(0)
	readerOutputTokens := int64(0)
	readerTotalTokens := int64(0)
	for _, result := range cases {
		searchLatencies = append(searchLatencies, result.SearchLatenciesMS...)
		if result.IndexWaitLatencyMS > 0 {
			waitLatencies = append(waitLatencies, result.IndexWaitLatencyMS)
		}
		contextTokens += result.EstimatedCtxTokens
		if result.Answer != nil {
			readerInputTokens += result.Answer.InputTokens
			readerOutputTokens += result.Answer.OutputTokens
			readerTotalTokens += result.Answer.TotalTokens
		}
	}

	hostname, _ := os.Hostname()
	report := &Report{
		SchemaVersion: SchemaVersion,
		Run: RunMetadata{
			HarnessVersion: HarnessVersion, GitSHA: config.GitSHA,
			RunID: startedAt.Format("20060102T150405.000000000Z"), StartedAt: startedAt,
			CompletedAt: time.Now().UTC(), Backend: r.backend.Name(),
			Plugin: config.Plugin, Project: config.Project, TopK: config.TopK, MinScore: config.MinScore,
			Concurrency: config.Concurrency, Warmup: config.Warmup, Repetitions: config.Repetitions,
			Seed: config.Seed, IndexTimeoutMS: config.IndexTimeout.Milliseconds(),
			PollIntervalMS: config.PollInterval.Milliseconds(), ProtocolVersion: config.ProtocolVersion,
			ReaderModel: config.ReaderModel, ReaderPrompt: readerPromptVersion(r.answerer),
			ConfigSHA256: runConfigHash(config), GoVersion: runtime.Version(),
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Hostname: hostname,
		},
		Dataset: DatasetMetadata{
			Name: normalizedDataset.Name, Version: normalizedDataset.Version, Source: normalizedDataset.Source,
			SHA256: normalizedDataset.SHA256, Documents: len(normalizedDataset.Documents), Queries: len(normalizedDataset.Queries),
		},
		Quality:    aggregateQuality(cases),
		Categories: aggregateCategories(cases),
		Cases:      cases,
		Warnings:   warnings,
		Operational: OperationalSummary{
			IngestLatency: ingestLatencies, SearchLatency: latencyStats(searchLatencies),
			IndexWaitLatency: latencyStats(waitLatencies),
			ReaderInputTokens: readerInputTokens, ReaderOutputTokens: readerOutputTokens,
			ReaderTotalTokens: readerTotalTokens,
		},
	}
	if ingestElapsed > 0 {
		report.Operational.DocumentsPerSecond = float64(len(normalizedDataset.Documents)) / ingestElapsed.Seconds()
	}
	if len(cases) > 0 {
		report.Operational.MeanContextTokens = float64(contextTokens) / float64(len(cases))
	}
	return report, nil
}

func normalizeRunConfig(config RunConfig) RunConfig {
	if config.Backend == "" {
		config.Backend = "mcp"
	}
	if config.Plugin == "" {
		config.Plugin = "rag"
	}
	if config.TopK <= 0 {
		config.TopK = 10
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 1
	}
	if config.Warmup < 0 {
		config.Warmup = 0
	}
	if config.Repetitions <= 0 {
		config.Repetitions = 1
	}
	if config.Seed == 0 {
		config.Seed = 42
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.ProtocolVersion == "" && config.Backend == "mcp" {
		config.ProtocolVersion = DefaultProtocolVersion
	}
	return config
}

func (r *Runner) ingest(ctx context.Context, documents []Document, config RunConfig) (LatencyStats, time.Duration, error) {
	started := time.Now()
	type job struct {
		index    int
		document Document
	}
	jobs := make(chan job)
	latencies := make([]float64, len(documents))
	errorsByIndex := make([]error, len(documents))
	workers := min(config.Concurrency, len(documents))
	if workers <= 0 {
		workers = 1
	}
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for current := range jobs {
				operationStarted := time.Now()
				err := r.backend.Write(ctx, config.Project, current.document)
				latencies[current.index] = millisecondsSince(operationStarted)
				errorsByIndex[current.index] = err
			}
		}()
	}
	for index, document := range documents {
		select {
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return LatencyStats{}, time.Since(started), errors.WithStack(ctx.Err())
		case jobs <- job{index: index, document: document}:
		}
	}
	close(jobs)
	group.Wait()
	failures := make([]string, 0)
	for index, err := range errorsByIndex {
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", documents[index].Path, err))
		}
	}
	if len(failures) > 0 {
		return latencyStats(latencies), time.Since(started), errors.Errorf("%d document writes failed: %s", len(failures), strings.Join(failures, "; "))
	}
	return latencyStats(latencies), time.Since(started), nil
}

type readinessResult struct {
	latencyMS float64
	attempts  int
	err       error
}

func (r *Runner) waitForRelevant(ctx context.Context, project string, query Query, config RunConfig) readinessResult {
	started := time.Now()
	waitCtx, cancel := context.WithTimeout(ctx, config.IndexTimeout)
	defer cancel()
	attempts := 0
	var lastErr error
	for {
		attempts++
		hits, err := r.backend.Search(waitCtx, project, query, config.TopK)
		if err == nil {
			hits = filterHits(hits, config.MinScore, config.TopK)
			if hasRelevant(query, hits) {
				return readinessResult{latencyMS: millisecondsSince(started), attempts: attempts}
			}
		} else {
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			message := "index readiness timeout"
			if lastErr != nil {
				message += ": " + lastErr.Error()
			}
			return readinessResult{
				latencyMS: millisecondsSince(started), attempts: attempts,
				err: errors.Errorf("%s for query %s", message, query.ID),
			}
		case <-time.After(config.PollInterval):
		}
	}
}

func (r *Runner) evaluateQueries(ctx context.Context, queries []Query, waits map[string]readinessResult, config RunConfig) []CaseResult {
	type job struct {
		index int
		query Query
	}
	jobs := make(chan job)
	results := make([]CaseResult, len(queries))
	workers := min(config.Concurrency, len(queries))
	if workers <= 0 {
		workers = 1
	}
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for current := range jobs {
				results[current.index] = r.evaluateQuery(ctx, current.query, waits[current.query.ID], config)
			}
		}()
	}
	for index, query := range queries {
		select {
		case <-ctx.Done():
			results[index] = CaseResult{QueryID: query.ID, Query: query.Text, Error: ctx.Err().Error()}
		case jobs <- job{index: index, query: query}:
		}
	}
	close(jobs)
	group.Wait()
	return results
}

func (r *Runner) evaluateQuery(ctx context.Context, query Query, readiness readinessResult, config RunConfig) CaseResult {
	result := CaseResult{
		QueryID: query.ID, Category: query.Category, Query: query.Text,
		GoldPaths: append([]string(nil), query.GoldPaths...),
		GoldEvidence: append([]string(nil), query.GoldEvidence...),
		Unanswerable: query.Unanswerable, IndexWaitLatencyMS: readiness.latencyMS,
		SearchAttempts: readiness.attempts,
	}
	if readiness.err != nil {
		result.Error = readiness.err.Error()
	}
	for warmup := 0; warmup < config.Warmup; warmup++ {
		if _, err := r.backend.Search(ctx, config.Project, query, config.TopK); err != nil {
			result.Error = appendError(result.Error, "warmup: "+err.Error())
			break
		}
		result.SearchAttempts++
	}
	var finalHits []SearchHit
	for repetition := 0; repetition < config.Repetitions; repetition++ {
		started := time.Now()
		hits, err := r.backend.Search(ctx, config.Project, query, config.TopK)
		latency := millisecondsSince(started)
		result.SearchLatenciesMS = append(result.SearchLatenciesMS, latency)
		result.SearchAttempts++
		if err != nil {
			result.Error = appendError(result.Error, err.Error())
			continue
		}
		finalHits = filterHits(hits, config.MinScore, config.TopK)
	}
	result.SearchLatencyMS = mean(result.SearchLatenciesMS)
	result.Retrieved = finalHits
	result.EstimatedCtxTokens = estimatedTokens(finalHits)
	if r.answerer != nil && result.Error == "" {
		answer, err := r.answerer.Answer(ctx, query, finalHits)
		if err != nil {
			result.Error = appendError(result.Error, "reader: "+err.Error())
		} else {
			result.Answer = answer
		}
	}
	result.Metrics = scoreCase(query, finalHits, result.Answer, config.TopK)
	return result
}

func (r *Runner) cleanup(ctx context.Context, documents []Document, config RunConfig) error {
	failures := make([]string, 0)
	for _, document := range documents {
		if err := r.backend.Delete(ctx, config.Project, document.Path, false); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", document.Path, err))
		}
	}
	if len(failures) > 0 {
		return errors.Errorf("benchmark cleanup failed for %d paths: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

func filterHits(hits []SearchHit, minScore float64, limit int) []SearchHit {
	filtered := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Score < minScore {
			continue
		}
		filtered = append(filtered, hit)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func hasRelevant(query Query, hits []SearchHit) bool {
	goldPaths := make(map[string]struct{}, len(query.GoldPaths))
	for _, path := range query.GoldPaths {
		goldPaths[path] = struct{}{}
	}
	for _, hit := range hits {
		if _, exists := goldPaths[hit.FilePath]; exists {
			return true
		}
	}
	if len(query.GoldEvidence) > 0 && evidenceRecall(query.GoldEvidence, hits) > 0 {
		return true
	}
	return false
}

func runConfigHash(config RunConfig) string {
	stable := struct {
		Backend, Plugin, ProtocolVersion, ReaderModel string
		TopK, Concurrency, Warmup, Repetitions       int
		MinScore                                     float64
		Seed                                         int64
		IndexTimeoutMS, PollIntervalMS                int64
	}{
		config.Backend, config.Plugin, config.ProtocolVersion, config.ReaderModel,
		config.TopK, config.Concurrency, config.Warmup, config.Repetitions,
		config.MinScore, config.Seed, config.IndexTimeout.Milliseconds(), config.PollInterval.Milliseconds(),
	}
	raw, _ := json.Marshal(stable)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readerPromptVersion(answerer Answerer) string {
	if answerer == nil {
		return ""
	}
	return ReaderPromptVersion
}

func appendError(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return existing
	}
	return existing + "; " + next
}

func millisecondsSince(start time.Time) float64 {
	return float64(time.Since(start).Nanoseconds()) / float64(time.Millisecond)
}

// SortedCases returns a stable query-id-sorted copy for external tooling.
func SortedCases(cases []CaseResult) []CaseResult {
	copyCases := append([]CaseResult(nil), cases...)
	sort.Slice(copyCases, func(i, j int) bool { return copyCases[i].QueryID < copyCases[j].QueryID })
	return copyCases
}
