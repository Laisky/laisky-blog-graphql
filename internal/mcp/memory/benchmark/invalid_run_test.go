package benchmark

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAggregateQualityExcludesFailedCases verifies execution failures are not
// converted into measured zero quality and cannot create false abstention credit.
func TestAggregateQualityExcludesFailedCases(t *testing.T) {
	t.Parallel()

	cases := []CaseResult{
		{
			QueryID: "successful-answerable",
			Metrics: CaseMetrics{
				RecallAtK:      1,
				PrecisionAtK:   0.2,
				NDCGAtK:        1,
				MRR:            1,
				HitAtK:         true,
				EvidenceRecall: 1,
			},
		},
		{
			QueryID: "failed-answerable",
			Error:   "index readiness timeout",
		},
		{
			QueryID:      "apparently-correct-abstention",
			Unanswerable: true,
			Metrics:      CaseMetrics{AbstentionOK: boolPointer(true)},
		},
	}

	summary := aggregateQuality(cases)
	require.Equal(t, 3, summary.Queries)
	require.Equal(t, 2, summary.EvaluatedQueries)
	require.Equal(t, 1, summary.FailedQueries)
	require.Equal(t, 1, summary.AnswerableQueries)
	require.Equal(t, 1, summary.UnanswerableQueries)
	require.True(t, summary.RetrievalMetricsAvailable)
	require.InDelta(t, 1.0, summary.RecallAtK, 1e-9)
	require.InDelta(t, 1.0, summary.NDCGAtK, 1e-9)
	require.InDelta(t, 1.0, summary.MRR, 1e-9)
	require.InDelta(t, 1.0, summary.HitRateAtK, 1e-9)
	require.InDelta(t, 1.0, summary.EvidenceRecall, 1e-9)
	require.False(t, summary.AbstentionMetricsAvailable, "an invalid run cannot receive abstention credit from an empty or broken index")
	require.InDelta(t, 1.0/3.0, summary.ErrorRate, 1e-9)
}

// TestInvalidScorecardUsesNAAndCannotBecomeBaseline verifies human and machine
// outputs distinguish execution failure from a successfully measured zero score.
func TestInvalidScorecardUsesNAAndCannotBecomeBaseline(t *testing.T) {
	t.Parallel()

	invalidCases := []CaseResult{
		{QueryID: "failed", Category: "retrieval", Error: "index readiness timeout"},
		{QueryID: "abstention", Category: "abstention", Unanswerable: true, Metrics: CaseMetrics{AbstentionOK: boolPointer(true)}},
	}
	invalid := &Report{
		SchemaVersion: SchemaVersion,
		Run:           RunMetadata{Plugin: "pageindex", TopK: 5, ConfigSHA256: "same"},
		Dataset:       DatasetMetadata{SHA256: "dataset", Queries: len(invalidCases)},
		Quality:       aggregateQuality(invalidCases),
		Categories:    aggregateCategories(invalidCases),
		Cases:         invalidCases,
	}

	var scorecard bytes.Buffer
	require.NoError(t, WriteScorecard(&scorecard, invalid, nil))
	require.Equal(t, ReportStatusInvalid, invalid.Status)
	require.Contains(t, scorecard.String(), "**Status:** `INVALID`")
	require.Contains(t, scorecard.String(), "| Recall@5 | n/a |")
	require.Contains(t, scorecard.String(), "| `failed` | retrieval | n/a | n/a | n/a | n/a |")
	require.NotContains(t, scorecard.String(), "| Abstention accuracy | 1.0000 |")

	validCases := []CaseResult{{
		QueryID: "ok",
		Metrics: CaseMetrics{RecallAtK: 1, PrecisionAtK: 1, NDCGAtK: 1, MRR: 1, HitAtK: true, EvidenceRecall: 1},
	}}
	valid := &Report{
		SchemaVersion: SchemaVersion,
		Run:           RunMetadata{Plugin: "pageindex", TopK: 5, ConfigSHA256: "same"},
		Dataset:       DatasetMetadata{SHA256: "dataset", Queries: len(validCases)},
		Quality:       aggregateQuality(validCases),
		Cases:         validCases,
	}
	comparison := CompareReports(valid, invalid, GateConfig{})
	require.False(t, comparison.Compatible)
	require.False(t, comparison.Passed)
	require.Contains(t, comparison.Reason, "candidate benchmark report is invalid")
}

// TestWriteArtifactsPersistsInvalidStatus verifies invalid diagnostic evidence
// is retained even though ValidateReport rejects it for CI and baseline use.
func TestWriteArtifactsPersistsInvalidStatus(t *testing.T) {
	t.Parallel()

	report := &Report{
		SchemaVersion: SchemaVersion,
		Run:           RunMetadata{Plugin: "pageindex", TopK: 5},
		Dataset:       DatasetMetadata{SHA256: "dataset", Queries: 1},
		Quality:       aggregateQuality([]CaseResult{{QueryID: "failed", Error: "backend unavailable"}}),
		Cases:         []CaseResult{{QueryID: "failed", Error: "backend unavailable"}},
	}
	dir := t.TempDir()
	require.NoError(t, WriteArtifacts(dir, report, nil))
	require.FileExists(t, filepath.Join(dir, "report.json"))
	require.FileExists(t, filepath.Join(dir, "scorecard.md"))
	require.Equal(t, ReportStatusInvalid, report.Status)
	require.Error(t, ValidateReport(report))
}
