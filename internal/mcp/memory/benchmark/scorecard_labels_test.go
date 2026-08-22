package benchmark

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScorecardDistinguishesRetrievalAndAnswerAbstention verifies that a
// retrieval-only run is never described as an LLM false-answer measurement.
func TestScorecardDistinguishesRetrievalAndAnswerAbstention(t *testing.T) {
	t.Parallel()

	newReport := func(readerModel string) *Report {
		cases := []CaseResult{{
			QueryID:      "unknown",
			Unanswerable: true,
			Metrics:      CaseMetrics{AbstentionOK: boolPointer(false)},
		}}
		return &Report{
			SchemaVersion: SchemaVersion,
			Run:           RunMetadata{Plugin: "rag", TopK: 5, ReaderModel: readerModel},
			Dataset:       DatasetMetadata{SHA256: "dataset", Queries: len(cases)},
			Quality:       aggregateQuality(cases),
			Categories:    aggregateCategories(cases),
			Cases:         cases,
		}
	}

	var retrievalOnly bytes.Buffer
	require.NoError(t, WriteScorecard(&retrievalOnly, newReport(""), nil))
	require.Contains(t, retrievalOnly.String(), "| Retrieval abstention accuracy | 0.0000 |")
	require.Contains(t, retrievalOnly.String(), "| Unexpected retrieval rate | 1.0000 |")
	require.NotContains(t, retrievalOnly.String(), "False-answer rate")

	var answerRun bytes.Buffer
	require.NoError(t, WriteScorecard(&answerRun, newReport("fixed-reader"), nil))
	require.Contains(t, answerRun.String(), "| Answer abstention accuracy | 0.0000 |")
	require.Contains(t, answerRun.String(), "| False-answer rate | 1.0000 |")
}
