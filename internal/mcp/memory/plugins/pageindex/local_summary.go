package pageindex

import (
	"strings"
	"unicode/utf8"
)

const (
	// localSummaryScanBytes bounds the text read to derive one local summary.
	localSummaryScanBytes = 4096
	// localSummaryMaxBytes bounds one local summary.
	localSummaryMaxBytes = 320
)

// localNodeSummary derives a deterministic summary from the opening words of a
// node's text, with Markdown block markers removed, when the LLM budget cannot
// pay for a generated one. It reads at most localSummaryScanBytes and returns at
// most localSummaryMaxBytes, cut on a word boundary.
func localNodeSummary(text string) string {
	text = prefixOnRuneBoundary(text, localSummaryScanBytes)
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "#>*-+ \t")
		for _, word := range strings.Fields(sanitizeInline(line)) {
			if b.Len()+len(word)+1 > localSummaryMaxBytes {
				if b.Len() == 0 {
					return prefixOnRuneBoundary(word, localSummaryMaxBytes)
				}
				return strings.TrimSpace(b.String())
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(word)
		}
	}
	return strings.TrimSpace(b.String())
}

// prefixOnRuneBoundary returns at most limit bytes of s without splitting a rune.
func prefixOnRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
