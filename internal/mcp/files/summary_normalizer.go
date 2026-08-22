package files

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Shared file-level summary limits, sources, and lifecycle states used by every
// plugin that publishes a file overview into file_search results. See
// docs/proposals/file_search_file_summaries.md §3.4 and §5.1.
const (
	// SummaryMaxWordsHard is the maximum number of Unicode word segments any
	// persisted or returned summary may contain. Operators may configure a lower
	// limit but may never raise this ceiling.
	SummaryMaxWordsHard = 300
	// SummaryMaxBytesHard is the maximum serialized UTF-8 size of any summary.
	SummaryMaxBytesHard = 2048
	// SummaryTargetWordsDefault is the default target length for substantive files.
	SummaryTargetWordsDefault = 160
)

// SummarySource identifies how a stored summary was produced. It is internal
// metadata and is never exposed through the public search response.
type SummarySource string

const (
	// SummarySourceModel marks a summary produced by the RAG summarizer model.
	SummarySourceModel SummarySource = "model"
	// SummarySourcePageIndex marks a summary produced by the PageIndex pipeline.
	SummarySourcePageIndex SummarySource = "pageindex"
	// SummarySourceDeterministicFallback marks a locally derived bounded fallback.
	SummarySourceDeterministicFallback SummarySource = "deterministic_fallback"
)

// SummaryStatus is the lifecycle state of a stored file summary.
type SummaryStatus string

const (
	// SummaryStatusPending marks a file whose summary has not been produced yet.
	SummaryStatusPending SummaryStatus = "pending"
	// SummaryStatusReady marks a validated model or PageIndex summary.
	SummaryStatusReady SummaryStatus = "ready"
	// SummaryStatusDegraded marks a deterministic fallback awaiting refresh.
	SummaryStatusDegraded SummaryStatus = "degraded"
	// SummaryStatusFailed marks a summary whose refresh terminally failed.
	SummaryStatusFailed SummaryStatus = "failed"
)

// Deterministic English fallbacks for empty and opaque content (§3.4).
const (
	summaryFallbackEmpty  = "Empty file."
	summaryFallbackOpaque = "This file contains encoded or non-natural-language text; no reliable semantic overview is available."
)

// SummaryWordCount counts Unicode word segments that contain at least one letter
// or number, following UAX #29 word segmentation. Whitespace- and punctuation-only
// segments are ignored so the count reflects human-visible words across scripts
// (including CJK, where each ideograph is typically its own segment).
func SummaryWordCount(s string) int {
	count := 0
	remaining := s
	state := -1
	for len(remaining) > 0 {
		var word string
		word, remaining, state = uniseg.FirstWordInString(remaining, state)
		if summarySegmentIsCountable(word) {
			count++
		}
	}
	return count
}

// summarySegmentIsCountable reports whether a UAX #29 word segment counts as a word.
func summarySegmentIsCountable(seg string) bool {
	for _, r := range seg {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// ClampSummaryLimits returns effective word and byte limits, never exceeding the
// hard maxima. A non-positive input selects the hard maximum.
func ClampSummaryLimits(maxWords, maxBytes int) (int, int) {
	if maxWords <= 0 || maxWords > SummaryMaxWordsHard {
		maxWords = SummaryMaxWordsHard
	}
	if maxBytes <= 0 || maxBytes > SummaryMaxBytesHard {
		maxBytes = SummaryMaxBytesHard
	}
	return maxWords, maxBytes
}

// NormalizeSummary validates and normalizes a candidate model summary against the
// shared format rules (§3.4). It collapses the candidate into a single trimmed
// paragraph, strips Markdown fences, rejects HTML/tool-call markup, and enforces the
// word and byte caps. When the candidate exceeds a cap it makes a single
// deterministic sentence-boundary truncation attempt. It returns the normalized
// text, its Unicode word count, and ok=false when no compliant summary can be
// produced — in which case the caller must publish a deterministic fallback.
func NormalizeSummary(raw string, maxWords, maxBytes int) (string, int, bool) {
	maxWords, maxBytes = ClampSummaryLimits(maxWords, maxBytes)
	text := collapseSummaryWhitespace(stripSummaryMarkup(raw))
	if text == "" {
		return "", 0, false
	}
	if summaryContainsForbiddenMarkup(text) {
		return "", 0, false
	}
	wc := SummaryWordCount(text)
	if wc <= maxWords && len(text) <= maxBytes {
		return text, wc, true
	}
	if truncated, ok := truncateSummaryToSentence(text, maxWords, maxBytes); ok {
		return truncated, SummaryWordCount(truncated), true
	}
	return "", 0, false
}

// DeterministicFileSummaryFallback builds a bounded, English, content-grounded
// fallback overview when model summarization is unavailable or invalid. It never
// includes the file path so a content-preserving rename can reuse it. The result
// always satisfies the effective limits and is therefore safe to persist and return.
func DeterministicFileSummaryFallback(content string, maxWords, maxBytes int) string {
	maxWords, maxBytes = ClampSummaryLimits(maxWords, maxBytes)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return summaryFallbackEmpty
	}
	if summaryLooksOpaque(trimmed) {
		return hardClampSummary(summaryFallbackOpaque, maxWords, maxBytes)
	}
	return hardClampSummary(buildDeterministicOverview(trimmed), maxWords, maxBytes)
}

// stripSummaryMarkup removes Markdown code fences the model may wrap output in.
func stripSummaryMarkup(raw string) string {
	if !strings.Contains(raw, "```") {
		return raw
	}
	// Drop fence lines entirely; keep inner content.
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// collapseSummaryWhitespace flattens the text to one trimmed paragraph.
func collapseSummaryWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// summaryContainsForbiddenMarkup reports whether the normalized text still carries
// disallowed HTML tags or fence markers (§3.4: no Markdown fence, HTML, or tool call).
func summaryContainsForbiddenMarkup(text string) bool {
	if strings.Contains(text, "```") {
		return true
	}
	// Detect an HTML/XML-ish tag: '<' + letter or '/' ... '>'.
	for i := 0; i < len(text)-1; i++ {
		if text[i] != '<' {
			continue
		}
		next := text[i+1]
		if next == '/' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') {
			if strings.IndexByte(text[i:], '>') > 0 {
				return true
			}
		}
	}
	return false
}

// truncateSummaryToSentence accumulates complete sentences until the next one would
// breach a cap, returning the accumulated text only when it ends at a sentence
// terminator. It reports ok=false when not even the first sentence fits.
func truncateSummaryToSentence(text string, maxWords, maxBytes int) (string, bool) {
	var b strings.Builder
	remaining := text
	state := -1
	for len(remaining) > 0 {
		var sentence string
		sentence, remaining, state = uniseg.FirstSentenceInString(remaining, state)
		candidate := strings.TrimSpace(b.String() + sentence)
		if SummaryWordCount(candidate) > maxWords || len(candidate) > maxBytes {
			break
		}
		b.WriteString(sentence)
	}
	result := strings.TrimSpace(b.String())
	if result == "" || !endsWithSentenceTerminator(result) {
		return "", false
	}
	return result, true
}

// endsWithSentenceTerminator reports whether the text ends at a sentence boundary.
func endsWithSentenceTerminator(text string) bool {
	r, _ := utf8.DecodeLastRuneInString(text)
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	default:
		return false
	}
}

// ClampSummaryText normalizes a candidate summary and, when it cannot be validated,
// hard-clamps it so the result always satisfies both caps. It never rejects a
// non-empty input, making it safe for defensive use on the search path over
// already-persisted summaries (e.g. legacy PageIndex descriptions).
func ClampSummaryText(raw string, maxWords, maxBytes int) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if text, _, ok := NormalizeSummary(raw, maxWords, maxBytes); ok {
		return text
	}
	return hardClampSummary(collapseSummaryWhitespace(stripSummaryMarkup(raw)), maxWords, maxBytes)
}

// hardClampSummary guarantees a string satisfies both caps by truncating on word and
// then rune boundaries. It is the last-resort clamp used only for deterministic
// fallbacks, which must never be rejected.
func hardClampSummary(text string, maxWords, maxBytes int) string {
	text = collapseSummaryWhitespace(text)
	if SummaryWordCount(text) > maxWords {
		text = truncateToWordLimit(text, maxWords)
	}
	if len(text) > maxBytes {
		text = truncateToByteLimit(text, maxBytes)
	}
	return strings.TrimSpace(text)
}

// truncateToWordLimit keeps at most maxWords countable word segments.
func truncateToWordLimit(text string, maxWords int) string {
	if maxWords <= 0 {
		return ""
	}
	count := 0
	consumed := 0
	remaining := text
	state := -1
	for len(remaining) > 0 {
		var word string
		word, remaining, state = uniseg.FirstWordInString(remaining, state)
		if summarySegmentIsCountable(word) {
			if count == maxWords {
				break
			}
			count++
		}
		consumed += len(word)
	}
	return strings.TrimSpace(text[:consumed])
}

// truncateToByteLimit truncates to at most maxBytes on a UTF-8 rune boundary.
func truncateToByteLimit(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut])
}

// buildDeterministicOverview derives a grounded, bounded English overview from the
// file content only (never the path).
func buildDeterministicOverview(content string) string {
	firstLine := sanitizeSummarySnippet(firstMeaningfulLine(content), 160)
	lineCount := strings.Count(content, "\n") + 1
	var b strings.Builder
	b.WriteString("Automated overview: this file opens with ")
	b.WriteString(strconv.Quote(firstLine))
	b.WriteString(" and contains approximately ")
	b.WriteString(strconv.Itoa(lineCount))
	if lineCount == 1 {
		b.WriteString(" line of text.")
	} else {
		b.WriteString(" lines of text.")
	}
	if terms := prominentSummaryTerms(content, 5); len(terms) > 0 {
		b.WriteString(" Frequent terms include ")
		b.WriteString(strings.Join(terms, ", "))
		b.WriteString(".")
	}
	return b.String()
}

// firstMeaningfulLine returns the first non-empty line with Markdown heading and
// list markers stripped.
func firstMeaningfulLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimLeft(trimmed, "#>*-+ \t")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(content)
}

// sanitizeSummarySnippet removes quotes/control characters and caps snippet length.
func sanitizeSummarySnippet(s string, maxRunes int) string {
	s = collapseSummaryWhitespace(s)
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == '`' || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	s = collapseSummaryWhitespace(s)
	runes := []rune(s)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return strings.TrimSpace(string(runes))
}

// prominentSummaryTerms returns up to n frequent significant lowercase tokens.
func prominentSummaryTerms(content string, n int) []string {
	counts := make(map[string]int)
	order := make([]string, 0, 16)
	for _, tok := range tokenize(content) {
		if len(tok) < 4 || summaryStopWords[tok] {
			continue
		}
		if counts[tok] == 0 {
			order = append(order, tok)
		}
		counts[tok]++
	}
	// Stable selection: sort by count desc, then first-seen order.
	rank := make(map[string]int, len(order))
	for i, tok := range order {
		rank[tok] = i
	}
	sortByFrequency(order, counts, rank)
	if len(order) > n {
		order = order[:n]
	}
	return order
}

// summaryLooksOpaque reports whether content reads as encoded or non-natural text.
func summaryLooksOpaque(content string) bool {
	hasSpace := strings.ContainsAny(content, " \t\n\r")
	letters, total := 0, 0
	for _, r := range content {
		total++
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if total == 0 {
		return true
	}
	if !hasSpace && total > 64 {
		return true
	}
	if total >= 32 && float64(letters)/float64(total) < 0.30 {
		return true
	}
	return false
}

// summaryStopWords holds common English words excluded from deterministic term lists.
var summaryStopWords = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "have": true,
	"will": true, "your": true, "which": true, "there": true, "their": true,
	"about": true, "would": true, "these": true, "other": true, "should": true,
	"could": true, "them": true, "then": true, "when": true, "what": true,
	"than": true, "into": true, "over": true, "also": true, "been": true,
	"were": true, "here": true, "some": true, "more": true, "must": true,
}

// sortByFrequency orders tokens by descending count, breaking ties by first-seen rank.
func sortByFrequency(tokens []string, counts, rank map[string]int) {
	for i := 1; i < len(tokens); i++ {
		for j := i; j > 0; j-- {
			a, b := tokens[j-1], tokens[j]
			if counts[b] > counts[a] || (counts[b] == counts[a] && rank[b] < rank[a]) {
				tokens[j-1], tokens[j] = tokens[j], tokens[j-1]
			} else {
				break
			}
		}
	}
}
