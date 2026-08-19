package benchmark

import (
	"math"
	"math/rand"
)

// CompareReports checks compatibility, applies metric thresholds, and runs a paired nDCG test.
func CompareReports(baseline, candidate *Report, config GateConfig) Comparison {
	if baseline == nil || candidate == nil {
		return Comparison{Compatible: false, Passed: false, Reason: "baseline and candidate reports are required"}
	}
	if reason := compatibilityReason(baseline, candidate); reason != "" {
		return Comparison{Compatible: false, Passed: false, Reason: reason}
	}
	config = normalizeGateConfig(config)
	metrics := []MetricDelta{
		higherIsBetter("recall_at_k", baseline.Quality.RecallAtK, candidate.Quality.RecallAtK, config.MaxRecallDrop),
		higherIsBetter("ndcg_at_k", baseline.Quality.NDCGAtK, candidate.Quality.NDCGAtK, config.MaxNDCGDrop),
		higherIsBetter("mrr", baseline.Quality.MRR, candidate.Quality.MRR, config.MaxMRRDrop),
		higherIsBetter("hit_rate_at_k", baseline.Quality.HitRateAtK, candidate.Quality.HitRateAtK, config.MaxHitRateDrop),
		higherIsBetter("evidence_recall", baseline.Quality.EvidenceRecall, candidate.Quality.EvidenceRecall, config.MaxEvidenceRecallDrop),
		lowerIsBetter("error_rate", baseline.Quality.ErrorRate, candidate.Quality.ErrorRate, config.MaxErrorRateIncrease, "absolute"),
		latencyDelta("search_p95_ms", baseline.Operational.SearchLatency.P95, candidate.Operational.SearchLatency.P95, config.MaxP95LatencyIncrease),
	}
	passed := true
	for _, metric := range metrics {
		if !metric.Passed {
			passed = false
		}
	}
	permutation := pairedNDCGPermutation(baseline.Cases, candidate.Cases, config)
	return Comparison{Compatible: true, Passed: passed, Metrics: metrics, Permutation: permutation}
}

func compatibilityReason(baseline, candidate *Report) string {
	switch {
	case baseline.SchemaVersion != candidate.SchemaVersion:
		return "report schema versions differ"
	case baseline.Dataset.SHA256 != candidate.Dataset.SHA256:
		return "dataset SHA-256 values differ"
	case baseline.Dataset.Queries != candidate.Dataset.Queries:
		return "dataset query counts differ"
	case baseline.Run.Plugin != candidate.Run.Plugin:
		return "plugin names differ"
	case baseline.Run.ConfigSHA256 != candidate.Run.ConfigSHA256:
		return "benchmark configuration hashes differ"
	default:
		return ""
	}
}

func normalizeGateConfig(config GateConfig) GateConfig {
	if config.MaxRecallDrop < 0 {
		config.MaxRecallDrop = 0
	}
	if config.MaxNDCGDrop < 0 {
		config.MaxNDCGDrop = 0
	}
	if config.MaxMRRDrop < 0 {
		config.MaxMRRDrop = 0
	}
	if config.MaxHitRateDrop < 0 {
		config.MaxHitRateDrop = 0
	}
	if config.MaxEvidenceRecallDrop < 0 {
		config.MaxEvidenceRecallDrop = 0
	}
	if config.MaxErrorRateIncrease < 0 {
		config.MaxErrorRateIncrease = 0
	}
	if config.MaxP95LatencyIncrease < 0 {
		config.MaxP95LatencyIncrease = 0
	}
	if config.PermutationIterations <= 0 {
		config.PermutationIterations = 10_000
	}
	if config.PermutationAlpha <= 0 || config.PermutationAlpha >= 1 {
		config.PermutationAlpha = 0.05
	}
	if config.Seed == 0 {
		config.Seed = 42
	}
	return config
}

func higherIsBetter(name string, baseline, candidate, limit float64) MetricDelta {
	drop := baseline - candidate
	return MetricDelta{
		Name: name, Baseline: baseline, Candidate: candidate, Delta: candidate - baseline,
		Limit: limit, Passed: drop <= limit, Direction: "higher-is-better", Unit: "absolute",
	}
}

func lowerIsBetter(name string, baseline, candidate, limit float64, unit string) MetricDelta {
	increase := candidate - baseline
	return MetricDelta{
		Name: name, Baseline: baseline, Candidate: candidate, Delta: increase,
		Limit: limit, Passed: increase <= limit, Direction: "lower-is-better", Unit: unit,
	}
}

func latencyDelta(name string, baseline, candidate, relativeLimit float64) MetricDelta {
	relative := 0.0
	if baseline > 0 {
		relative = candidate/baseline - 1
	} else if candidate > 0 {
		relative = math.Inf(1)
	}
	return MetricDelta{
		Name: name, Baseline: baseline, Candidate: candidate, Delta: relative,
		Limit: relativeLimit, Passed: relative <= relativeLimit,
		Direction: "lower-is-better", Unit: "relative",
	}
}

func pairedNDCGPermutation(baselineCases, candidateCases []CaseResult, config GateConfig) *PermutationResult {
	baselineByID := make(map[string]CaseResult, len(baselineCases))
	for _, result := range baselineCases {
		if result.Error == "" && !result.Unanswerable {
			baselineByID[result.QueryID] = result
		}
	}
	differences := make([]float64, 0, len(candidateCases))
	for _, candidate := range candidateCases {
		baseline, exists := baselineByID[candidate.QueryID]
		if !exists || candidate.Error != "" || candidate.Unanswerable {
			continue
		}
		differences = append(differences, candidate.Metrics.NDCGAtK-baseline.Metrics.NDCGAtK)
	}
	if len(differences) == 0 {
		return nil
	}
	observed := mean(differences)
	random := rand.New(rand.NewSource(config.Seed)) //nolint:gosec // deterministic statistical resampling, not security-sensitive
	extreme := 0
	for iteration := 0; iteration < config.PermutationIterations; iteration++ {
		total := 0.0
		for _, difference := range differences {
			if random.Intn(2) == 0 {
				total -= difference
			} else {
				total += difference
			}
		}
		permuted := total / float64(len(differences))
		if math.Abs(permuted) >= math.Abs(observed) {
			extreme++
		}
	}
	pValue := float64(extreme+1) / float64(config.PermutationIterations+1)
	return &PermutationResult{
		Pairs: len(differences), Iterations: config.PermutationIterations,
		MeanDelta: observed, PValue: pValue, Alpha: config.PermutationAlpha,
		Significant: pValue < config.PermutationAlpha,
	}
}
