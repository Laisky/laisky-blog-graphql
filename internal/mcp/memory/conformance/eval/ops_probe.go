package eval

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// Billing is the per-call cost telemetry extracted from a plugin response.
type Billing struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	USD          float64
}

// BillingExtractor lets future plugins expose billing metadata via their
// SearchResult; the Phase-1 rag plugin returns zeros.
type BillingExtractor func(files.SearchResult) Billing

// OpsReport carries the latency and billing rollup for a search-replay run.
type OpsReport struct {
	NumQueries       int     `json:"n"`
	P50LatencyMS     int64   `json:"p50_latency_ms"`
	P95LatencyMS     int64   `json:"p95_latency_ms"`
	P99LatencyMS     int64   `json:"p99_latency_ms"`
	MeanLatencyMS    float64 `json:"mean_latency_ms"`
	MeanInputTokens  float64 `json:"mean_input_tokens"`
	MeanOutputTokens float64 `json:"mean_output_tokens"`
	MeanTotalTokens  float64 `json:"mean_total_tokens"`
	MeanUSDPerSearch float64 `json:"mean_usd_per_search"`
	UsdPer1KSearches float64 `json:"usd_per_1k_searches"`
}

// IngestionReport summarizes write→searchable latency.
type IngestionReport struct {
	NumDocs        int     `json:"n"`
	DocsPerMinute  float64 `json:"docs_per_minute"`
	PagesPerMinute float64 `json:"pages_per_minute"`
	MeanRoundTripS float64 `json:"mean_round_trip_s"`
	P95RoundTripS  float64 `json:"p95_round_trip_s"`
}

// DifferentialReport reports cold (1st call) vs warm (Nth call) latencies.
type DifferentialReport struct {
	ColdMS  int64 `json:"cold_ms"`
	WarmMS  int64 `json:"warm_ms"`
	DeltaMS int64 `json:"delta_ms"`
}

// OpsDoc is one ingestion-probe doc.
type OpsDoc struct {
	Project   string
	Path      string
	Auth      files.AuthContext
	Content   string
	Pages     int
	Query     string
	Confirm   func(files.SearchResult) bool
	MaxWait   time.Duration
	PollEvery time.Duration
}

// RunOpsProbe replays each query `replays` times against `Search` and returns
// the latency / billing rollup. `extract` may be nil; in that case zeros are
// reported for billing.
func RunOpsProbe(ctx context.Context, p mcpplugin.Plugin, queries []string, replays int, extract BillingExtractor) (OpsReport, error) {
	if p == nil {
		return OpsReport{}, errors.New("plugin is nil")
	}
	if replays <= 0 {
		replays = 1
	}
	auth := files.AuthContext{APIKey: "ops-probe", APIKeyHash: "ops-probe", UserIdentity: "user:ops-probe"}
	latencies := make([]int64, 0, len(queries)*replays)
	var inSum, outSum, totalSum int
	var usdSum float64
	billed := 0
	for r := 0; r < replays; r++ {
		for _, q := range queries {
			started := time.Now()
			res, err := p.Search(ctx, auth, "ops-probe", q, "", 10)
			latencies = append(latencies, time.Since(started).Milliseconds())
			if err != nil {
				continue
			}
			if extract != nil {
				b := extract(res)
				inSum += b.InputTokens
				outSum += b.OutputTokens
				totalSum += b.TotalTokens
				usdSum += b.USD
				billed++
			}
		}
	}
	rep := OpsReport{NumQueries: len(latencies)}
	if len(latencies) > 0 {
		rep.P50LatencyMS = percentileInt64(latencies, 0.50)
		rep.P95LatencyMS = percentileInt64(latencies, 0.95)
		rep.P99LatencyMS = percentileInt64(latencies, 0.99)
		var sum int64
		for _, v := range latencies {
			sum += v
		}
		rep.MeanLatencyMS = float64(sum) / float64(len(latencies))
	}
	if billed > 0 {
		rep.MeanInputTokens = float64(inSum) / float64(billed)
		rep.MeanOutputTokens = float64(outSum) / float64(billed)
		rep.MeanTotalTokens = float64(totalSum) / float64(billed)
		rep.MeanUSDPerSearch = usdSum / float64(billed)
		rep.UsdPer1KSearches = rep.MeanUSDPerSearch * 1000.0
	}
	return rep, nil
}

// RunIngestionProbe writes each doc and times until the doc is searchable.
func RunIngestionProbe(ctx context.Context, p mcpplugin.Plugin, docs []OpsDoc) (IngestionReport, error) {
	if p == nil {
		return IngestionReport{}, errors.New("plugin is nil")
	}
	rep := IngestionReport{NumDocs: len(docs)}
	durations := make([]int64, 0, len(docs))
	totalPages := 0
	totalElapsed := time.Duration(0)
	for _, d := range docs {
		started := time.Now()
		_, err := p.Write(ctx, d.Auth, d.Project, d.Path, d.Content, "utf-8", 0, files.WriteModeTruncate)
		if err != nil {
			continue
		}
		maxWait := d.MaxWait
		if maxWait == 0 {
			maxWait = 30 * time.Second
		}
		poll := d.PollEvery
		if poll == 0 {
			poll = 200 * time.Millisecond
		}
		deadline := started.Add(maxWait)
		var saw bool
		for time.Now().Before(deadline) {
			res, err := p.Search(ctx, d.Auth, d.Project, d.Query, "", 10)
			if err == nil {
				if d.Confirm == nil {
					if len(res.Chunks) > 0 {
						saw = true
						break
					}
				} else if d.Confirm(res) {
					saw = true
					break
				}
			}
			time.Sleep(poll)
		}
		elapsed := time.Since(started)
		durations = append(durations, elapsed.Milliseconds())
		if saw {
			totalPages += d.Pages
			totalElapsed += elapsed
		}
	}
	if len(durations) > 0 {
		sum := int64(0)
		for _, v := range durations {
			sum += v
		}
		rep.MeanRoundTripS = float64(sum) / float64(len(durations)) / 1000.0
		rep.P95RoundTripS = float64(percentileInt64(durations, 0.95)) / 1000.0
	}
	if totalElapsed > 0 {
		minutes := totalElapsed.Minutes()
		if minutes > 0 {
			rep.PagesPerMinute = float64(totalPages) / minutes
			rep.DocsPerMinute = float64(len(durations)) / minutes
		}
	}
	return rep, nil
}

// RunColdWarmDifferential measures the very first search latency vs the 100th.
func RunColdWarmDifferential(ctx context.Context, p mcpplugin.Plugin, queries []string) (DifferentialReport, error) {
	if p == nil {
		return DifferentialReport{}, errors.New("plugin is nil")
	}
	if len(queries) == 0 {
		return DifferentialReport{}, errors.New("at least one query required")
	}
	auth := files.AuthContext{APIKey: "ops-probe", APIKeyHash: "ops-probe", UserIdentity: "user:ops-probe"}

	first := time.Now()
	_, _ = p.Search(ctx, auth, "ops-probe", queries[0], "", 10)
	cold := time.Since(first).Milliseconds()

	for i := 0; i < 99; i++ {
		_, _ = p.Search(ctx, auth, "ops-probe", queries[i%len(queries)], "", 10)
	}
	last := time.Now()
	_, _ = p.Search(ctx, auth, "ops-probe", queries[0], "", 10)
	warm := time.Since(last).Milliseconds()

	return DifferentialReport{ColdMS: cold, WarmMS: warm, DeltaMS: cold - warm}, nil
}
