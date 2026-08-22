package files

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestSummaryWordCountUnicode covers ASCII, CJK, and mixed-language segmentation (C06).
func TestSummaryWordCountUnicode(t *testing.T) {
	require.Equal(t, 0, SummaryWordCount(""))
	require.Equal(t, 0, SummaryWordCount("   \n\t "))
	require.Equal(t, 3, SummaryWordCount("hello brave world"))
	require.Equal(t, 3, SummaryWordCount("hello, brave, world!"))
	// Each Han ideograph is its own word segment under UAX #29.
	require.Equal(t, 4, SummaryWordCount("数据 检索"))
	// Mixed script: two English words plus four ideographs.
	require.Equal(t, 6, SummaryWordCount("data 数据 retrieval 检索"))
}

// TestNormalizeSummaryValid keeps a compliant paragraph unchanged except whitespace (C01).
func TestNormalizeSummaryValid(t *testing.T) {
	raw := "  This incident review explains the retry-queue saturation event\n and its mitigation.  "
	text, wc, ok := NormalizeSummary(raw, SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.True(t, ok)
	require.Equal(t, "This incident review explains the retry-queue saturation event and its mitigation.", text)
	require.Equal(t, SummaryWordCount(text), wc)
	require.NotContains(t, text, "\n")
}

// TestNormalizeSummaryStripsFencesAndRejectsMarkup enforces §3.4 markup rules (F03).
func TestNormalizeSummaryStripsFencesAndRejectsMarkup(t *testing.T) {
	text, _, ok := NormalizeSummary("```\nA plain grounded summary sentence.\n```", SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.True(t, ok)
	require.Equal(t, "A plain grounded summary sentence.", text)

	_, _, ok = NormalizeSummary("<script>alert(1)</script> summary", SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.False(t, ok, "HTML markup must be rejected")

	_, _, ok = NormalizeSummary("   ", SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.False(t, ok, "empty output must be rejected")
}

// TestNormalizeSummaryWordBoundary covers 299/300/301 words (C04).
func TestNormalizeSummaryWordBoundary(t *testing.T) {
	sentence := func(n int) string {
		words := make([]string, n)
		for i := range words {
			words[i] = "word"
		}
		return strings.Join(words, " ") + "."
	}

	_, wc, ok := NormalizeSummary(sentence(299), SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.True(t, ok)
	require.Equal(t, 299, wc)

	_, wc, ok = NormalizeSummary(sentence(300), SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.True(t, ok)
	require.Equal(t, 300, wc)

	// 301 words with no earlier sentence boundary cannot be truncated to a complete
	// sentence, so it is rejected (the caller then uses a deterministic fallback).
	text, wc, ok := NormalizeSummary(sentence(301), SummaryMaxWordsHard, SummaryMaxBytesHard)
	if ok {
		require.LessOrEqual(t, wc, 300)
		require.LessOrEqual(t, SummaryWordCount(text), 300)
	}
}

// TestNormalizeSummaryByteTruncation enforces the byte cap on a sentence boundary (C05).
func TestNormalizeSummaryByteTruncation(t *testing.T) {
	// Two sentences; the first alone is under 2048 bytes, both together exceed it.
	// The second sentence starts with a capital letter so UAX #29 detects the break
	// (it does not break before a lowercase continuation).
	first := "This is the first grounded sentence about the document topic."
	second := "Second sentence " + strings.Repeat("padding word ", 300) + "tail." // ~3.9 KiB
	raw := first + " " + second
	text, _, ok := NormalizeSummary(raw, SummaryMaxWordsHard, 2048)
	require.True(t, ok)
	require.LessOrEqual(t, len(text), 2048)
	require.True(t, utf8.ValidString(text))
	require.Equal(t, first, text)
}

// TestClampSummaryLimitsHardMax ensures configured limits never exceed the hard caps.
func TestClampSummaryLimitsHardMax(t *testing.T) {
	w, b := ClampSummaryLimits(9999, 999999)
	require.Equal(t, SummaryMaxWordsHard, w)
	require.Equal(t, SummaryMaxBytesHard, b)
	w, b = ClampSummaryLimits(0, 0)
	require.Equal(t, SummaryMaxWordsHard, w)
	require.Equal(t, SummaryMaxBytesHard, b)
	w, b = ClampSummaryLimits(120, 1024)
	require.Equal(t, 120, w)
	require.Equal(t, 1024, b)
}

// TestDeterministicFallbackBounded covers empty, opaque, and text content (C07).
func TestDeterministicFallbackBounded(t *testing.T) {
	require.Equal(t, "Empty file.", DeterministicFileSummaryFallback("", SummaryMaxWordsHard, SummaryMaxBytesHard))

	opaque := strings.Repeat("A1b2C3d4", 40) // long single token, no whitespace
	got := DeterministicFileSummaryFallback(opaque, SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.Contains(t, got, "encoded or non-natural-language")

	text := "# Incident Review\n\nThe retry queue reached saturation after a deploy.\nCustomers saw elevated latency."
	got = DeterministicFileSummaryFallback(text, SummaryMaxWordsHard, SummaryMaxBytesHard)
	require.NotEmpty(t, got)
	require.LessOrEqual(t, SummaryWordCount(got), SummaryMaxWordsHard)
	require.LessOrEqual(t, len(got), SummaryMaxBytesHard)
	// Grounded: mentions a real prominent term from the file.
	require.Contains(t, strings.ToLower(got), "retry")
}

// TestDeterministicFallbackRespectsLowerLimits ensures configured lower caps hold.
func TestDeterministicFallbackRespectsLowerLimits(t *testing.T) {
	text := strings.Repeat("saturation retry queue deploy latency ", 50)
	got := DeterministicFileSummaryFallback(text, 20, 200)
	require.LessOrEqual(t, SummaryWordCount(got), 20)
	require.LessOrEqual(t, len(got), 200)
	require.True(t, utf8.ValidString(got))
}
