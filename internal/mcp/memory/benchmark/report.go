package benchmark

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

// WriteArtifacts writes report.json, cases.jsonl, scorecard.md, and optional comparison artifacts.
func WriteArtifacts(dir string, report *Report, comparison *Comparison) error {
	if report == nil {
		return errors.New("benchmark report is nil")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.Wrapf(err, "create benchmark output directory %s", dir)
	}
	reportJSON, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encode benchmark report")
	}
	reportJSON = append(reportJSON, '\n')
	if err := writeAtomic(filepath.Join(dir, "report.json"), reportJSON); err != nil {
		return errors.Wrap(err, "write report.json")
	}
	if err := writeCases(filepath.Join(dir, "cases.jsonl"), report.Cases); err != nil {
		return errors.Wrap(err, "write cases.jsonl")
	}
	var scorecard bytes.Buffer
	if err := WriteScorecard(&scorecard, report, comparison); err != nil {
		return errors.Wrap(err, "render scorecard")
	}
	if err := writeAtomic(filepath.Join(dir, "scorecard.md"), scorecard.Bytes()); err != nil {
		return errors.Wrap(err, "write scorecard.md")
	}
	if comparison != nil {
		comparisonJSON, marshalErr := json.MarshalIndent(comparison, "", "  ")
		if marshalErr != nil {
			return errors.Wrap(marshalErr, "encode benchmark comparison")
		}
		comparisonJSON = append(comparisonJSON, '\n')
		if err := writeAtomic(filepath.Join(dir, "comparison.json"), comparisonJSON); err != nil {
			return errors.Wrap(err, "write comparison.json")
		}
		var comparisonMarkdown bytes.Buffer
		writeComparison(&comparisonMarkdown, comparison)
		if err := writeAtomic(filepath.Join(dir, "comparison.md"), comparisonMarkdown.Bytes()); err != nil {
			return errors.Wrap(err, "write comparison.md")
		}
	}
	return nil
}

// LoadReport reads and validates a persisted benchmark report.
func LoadReport(path string) (*Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read benchmark report %s", path)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, errors.Wrapf(err, "decode benchmark report %s", path)
	}
	if report.SchemaVersion == "" {
		return nil, errors.Errorf("benchmark report %s has no schema_version", path)
	}
	return &report, nil
}

// WriteScorecard renders a deterministic human-readable benchmark report.
func WriteScorecard(builder *bytes.Buffer, report *Report, comparison *Comparison) error {
	if builder == nil || report == nil {
		return errors.New("scorecard writer and report are required")
	}
	fmt.Fprintf(builder, "# MCP Memory Benchmark — %s\n\n", report.Run.Plugin)
	fmt.Fprintf(builder, "- **Run:** `%s`\n", markdownEscape(report.Run.RunID))
	fmt.Fprintf(builder, "- **Backend:** `%s`\n", markdownEscape(report.Run.Backend))
	fmt.Fprintf(builder, "- **Dataset:** `%s` (`%s`, SHA-256 `%s`)\n", markdownEscape(report.Dataset.Name), markdownEscape(report.Dataset.Version), markdownEscape(report.Dataset.SHA256))
	fmt.Fprintf(builder, "- **Plugin:** `%s`\n", markdownEscape(report.Run.Plugin))
	fmt.Fprintf(builder, "- **Configuration:** top-k `%d`, min-score `%.4f`, concurrency `%d`, warm-up `%d`, repetitions `%d`, seed `%d`\n", report.Run.TopK, report.Run.MinScore, report.Run.Concurrency, report.Run.Warmup, report.Run.Repetitions, report.Run.Seed)
	fmt.Fprintf(builder, "- **Config SHA-256:** `%s`\n", markdownEscape(report.Run.ConfigSHA256))
	if report.Run.ReaderModel != "" {
		fmt.Fprintf(builder, "- **Fixed reader:** `%s` with prompt `%s`\n", markdownEscape(report.Run.ReaderModel), markdownEscape(report.Run.ReaderPrompt))
	} else {
		fmt.Fprintln(builder, "- **Fixed reader:** disabled; answer metrics are not reported")
	}
	fmt.Fprintln(builder)

	fmt.Fprintln(builder, "## Quality")
	fmt.Fprintln(builder)
	fmt.Fprintln(builder, "| Metric | Value |")
	fmt.Fprintln(builder, "|---|---:|")
	writeMetricRow(builder, fmt.Sprintf("Recall@%d", report.Run.TopK), report.Quality.RecallAtK)
	writeMetricRow(builder, fmt.Sprintf("Precision@%d", report.Run.TopK), report.Quality.PrecisionAtK)
	writeMetricRow(builder, fmt.Sprintf("nDCG@%d", report.Run.TopK), report.Quality.NDCGAtK)
	writeMetricRow(builder, "MRR", report.Quality.MRR)
	writeMetricRow(builder, fmt.Sprintf("Hit rate@%d", report.Run.TopK), report.Quality.HitRateAtK)
	writeMetricRow(builder, "Evidence recall", report.Quality.EvidenceRecall)
	if report.Quality.AbstentionMetricsAvailable {
		writeMetricRow(builder, "Abstention accuracy", report.Quality.AbstentionAccuracy)
		writeMetricRow(builder, "False-answer rate", report.Quality.FalseAnswerRate)
	}
	if report.Quality.AnswerMetricsAvailable {
		writeMetricRow(builder, "Exact match", report.Quality.ExactMatch)
		writeMetricRow(builder, "Token precision", report.Quality.TokenPrecision)
		writeMetricRow(builder, "Token recall", report.Quality.TokenRecall)
		writeMetricRow(builder, "Token F1", report.Quality.TokenF1)
	}
	if report.Quality.RubricMetricsAvailable {
		writeMetricRow(builder, "Rubric coverage", report.Quality.RubricCoverage)
	}
	writeMetricRow(builder, "Error rate", report.Quality.ErrorRate)
	fmt.Fprintln(builder)

	fmt.Fprintln(builder, "## Ability slices")
	fmt.Fprintln(builder)
	fmt.Fprintf(builder, "| Category | N | Recall@%d | nDCG@%d | MRR | Hit@%d | Evidence | Abstention | Errors |\n", report.Run.TopK, report.Run.TopK, report.Run.TopK)
	fmt.Fprintln(builder, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, category := range report.Categories {
		quality := category.Quality
		fmt.Fprintf(builder, "| %s | %d | %.4f | %.4f | %.4f | %.4f | %.4f | %s | %.4f |\n",
			markdownEscape(category.Category), quality.Queries, quality.RecallAtK, quality.NDCGAtK,
			quality.MRR, quality.HitRateAtK, quality.EvidenceRecall,
			optionalMetric(quality.AbstentionMetricsAvailable, quality.AbstentionAccuracy), quality.ErrorRate)
	}
	fmt.Fprintln(builder)

	fmt.Fprintln(builder, "## Operations")
	fmt.Fprintln(builder)
	fmt.Fprintln(builder, "| Measurement | Mean | P50 | P95 | P99 | Max |")
	fmt.Fprintln(builder, "|---|---:|---:|---:|---:|---:|")
	writeLatencyRow(builder, "file_write (ms)", report.Operational.IngestLatency)
	writeLatencyRow(builder, "file_search (ms)", report.Operational.SearchLatency)
	writeLatencyRow(builder, "write→relevant (ms)", report.Operational.IndexWaitLatency)
	fmt.Fprintln(builder)
	fmt.Fprintf(builder, "- Documents per second: `%.3f`\n", report.Operational.DocumentsPerSecond)
	fmt.Fprintf(builder, "- Mean retrieved context tokens (estimated): `%.1f`\n", report.Operational.MeanContextTokens)
	if report.Operational.ReaderTotalTokens > 0 {
		fmt.Fprintf(builder, "- Reader tokens: input `%d`, output `%d`, total `%d`\n", report.Operational.ReaderInputTokens, report.Operational.ReaderOutputTokens, report.Operational.ReaderTotalTokens)
	}
	fmt.Fprintln(builder)

	fmt.Fprintln(builder, "## Case results")
	fmt.Fprintln(builder)
	fmt.Fprintf(builder, "| Query | Category | Recall@%d | nDCG@%d | MRR | Evidence | Search mean ms | Error |\n", report.Run.TopK, report.Run.TopK)
	fmt.Fprintln(builder, "|---|---|---:|---:|---:|---:|---:|---|")
	for _, result := range SortedCases(report.Cases) {
		fmt.Fprintf(builder, "| `%s` | %s | %.4f | %.4f | %.4f | %.4f | %.3f | %s |\n",
			markdownEscape(result.QueryID), markdownEscape(result.Category), result.Metrics.RecallAtK,
			result.Metrics.NDCGAtK, result.Metrics.MRR, result.Metrics.EvidenceRecall,
			result.SearchLatencyMS, markdownEscape(result.Error))
	}
	fmt.Fprintln(builder)

	if len(report.Warnings) > 0 {
		fmt.Fprintln(builder, "## Warnings")
		fmt.Fprintln(builder)
		for _, warning := range report.Warnings {
			fmt.Fprintf(builder, "- %s\n", markdownEscape(warning))
		}
		fmt.Fprintln(builder)
	}
	if comparison != nil {
		fmt.Fprintln(builder, "## Baseline comparison")
		fmt.Fprintln(builder)
		writeComparison(builder, comparison)
	}
	return nil
}

func writeComparison(builder *bytes.Buffer, comparison *Comparison) {
	fmt.Fprintf(builder, "- Compatible: `%t`\n", comparison.Compatible)
	fmt.Fprintf(builder, "- Gate passed: `%t`\n", comparison.Passed)
	if comparison.Reason != "" {
		fmt.Fprintf(builder, "- Reason: %s\n", markdownEscape(comparison.Reason))
	}
	if len(comparison.Metrics) > 0 {
		fmt.Fprintln(builder)
		fmt.Fprintln(builder, "| Metric | Baseline | Candidate | Delta | Limit | Direction | Passed |")
		fmt.Fprintln(builder, "|---|---:|---:|---:|---:|---|---:|")
		for _, metric := range comparison.Metrics {
			fmt.Fprintf(builder, "| %s | %.6f | %.6f | %.6f | %.6f | %s | %t |\n",
				markdownEscape(metric.Name), metric.Baseline, metric.Candidate, metric.Delta,
				metric.Limit, markdownEscape(metric.Direction), metric.Passed)
		}
	}
	if comparison.Permutation != nil {
		permutation := comparison.Permutation
		fmt.Fprintf(builder, "\nPaired nDCG sign-flip test: pairs `%d`, iterations `%d`, mean delta `%.6f`, p-value `%.6f`, alpha `%.4f`, significant `%t`.\n",
			permutation.Pairs, permutation.Iterations, permutation.MeanDelta,
			permutation.PValue, permutation.Alpha, permutation.Significant)
	}
}

func writeMetricRow(builder *bytes.Buffer, name string, value float64) {
	fmt.Fprintf(builder, "| %s | %.4f |\n", markdownEscape(name), value)
}

func writeLatencyRow(builder *bytes.Buffer, name string, stats LatencyStats) {
	fmt.Fprintf(builder, "| %s | %.3f | %.3f | %.3f | %.3f | %.3f |\n", markdownEscape(name), stats.Mean, stats.P50, stats.P95, stats.P99, stats.Max)
}

func optionalMetric(available bool, value float64) string {
	if !available {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", value)
}

func writeCases(path string, cases []CaseResult) error {
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	encoder := json.NewEncoder(writer)
	for _, result := range SortedCases(cases) {
		if err := encoder.Encode(result); err != nil {
			return errors.Wrap(err, "encode benchmark case")
		}
	}
	if err := writer.Flush(); err != nil {
		return errors.Wrap(err, "flush benchmark cases")
	}
	return writeAtomic(path, output.Bytes())
}

func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".memory-benchmark-*")
	if err != nil {
		return errors.Wrapf(err, "create temporary benchmark artifact for %s", path)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return errors.Wrapf(err, "write benchmark artifact %s", path)
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return errors.Wrapf(err, "chmod benchmark artifact %s", path)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.Wrapf(err, "sync benchmark artifact %s", path)
	}
	if err := file.Close(); err != nil {
		return errors.Wrapf(err, "close benchmark artifact %s", path)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return errors.Wrapf(err, "replace benchmark artifact %s", path)
	}
	return nil
}

func markdownEscape(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

// SortedCategoryNames returns stable category names from a report.
func SortedCategoryNames(report *Report) []string {
	if report == nil {
		return nil
	}
	result := make([]string, 0, len(report.Categories))
	for _, category := range report.Categories {
		result = append(result, category.Category)
	}
	sort.Strings(result)
	return result
}
