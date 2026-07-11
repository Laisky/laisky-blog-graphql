package pageindex

import (
	"strings"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// docSummaryMaxWords and docSummaryMaxBytes are the shared hard caps applied to every
// PageIndex document description (docs/proposals/file_search_file_summaries.md §4.5).
const (
	docSummaryMaxWords = files.SummaryMaxWordsHard
	docSummaryMaxBytes = files.SummaryMaxBytesHard
	// syntheticRootTitle is the placeholder title runMarkdown assigns to a document
	// with no headings; it carries no descriptive value and is skipped when deriving.
	syntheticRootTitle = "Document"
)

// finalizeDocDescription returns the bounded, English document description for a tree
// and the summary source that produced it. It prefers an existing (PDF or preset)
// description, then a deterministic description derived from the completed tree, and
// finally the shared deterministic fallback over the raw content. The result always
// satisfies the shared word/byte limits.
func finalizeDocDescription(tree *Tree, fullContent []byte) (string, files.SummarySource) {
	if tree != nil && strings.TrimSpace(tree.DocDescription) != "" {
		if text, _, ok := files.NormalizeSummary(tree.DocDescription, docSummaryMaxWords, docSummaryMaxBytes); ok {
			return text, files.SummarySourcePageIndex
		}
	}
	if tree != nil {
		if candidate := deriveTreeDescription(tree); candidate != "" {
			if text, _, ok := files.NormalizeSummary(candidate, docSummaryMaxWords, docSummaryMaxBytes); ok {
				return text, files.SummarySourcePageIndex
			}
		}
	}
	return files.DeterministicFileSummaryFallback(string(fullContent), docSummaryMaxWords, docSummaryMaxBytes), files.SummarySourceDeterministicFallback
}

// deriveTreeDescription builds a grounded English overview from the completed tree
// structure (section titles and, when present, node summaries), rather than relying
// on a single leaf-node summary (§4.5).
func deriveTreeDescription(tree *Tree) string {
	if tree == nil || len(tree.Structure) == 0 {
		return ""
	}
	titles := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	WalkNodes(tree.Structure, func(n *Node) {
		if len(titles) >= 8 {
			return
		}
		title := strings.TrimSpace(n.Title)
		if title == "" || title == syntheticRootTitle {
			return
		}
		if _, ok := seen[title]; ok {
			return
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
	})

	var b strings.Builder
	b.WriteString("This document")
	if name := strings.TrimSpace(tree.DocName); name != "" {
		b.WriteString(" titled ")
		b.WriteString(quoteSnippet(name))
	}
	if len(titles) > 0 {
		b.WriteString(" covers the following sections: ")
		b.WriteString(strings.Join(titles, "; "))
		b.WriteString(".")
	} else {
		b.WriteString(" has no explicit section headings.")
	}
	// Include the first available node summary as grounded evidence, if any.
	if lead := firstNodeSummary(tree.Structure); lead != "" {
		b.WriteString(" ")
		b.WriteString(quoteSummarySentence(lead))
	}
	return b.String()
}

// firstNodeSummary returns the first non-empty node summary in pre-order.
func firstNodeSummary(nodes []*Node) string {
	var found string
	WalkNodes(nodes, func(n *Node) {
		if found != "" {
			return
		}
		if s := strings.TrimSpace(n.Summary); s != "" {
			found = s
		}
	})
	return found
}

// quoteSnippet returns a sanitized, quoted, length-capped snippet.
func quoteSnippet(s string) string {
	s = sanitizeInline(s)
	runes := []rune(s)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return "\"" + strings.TrimSpace(string(runes)) + "\""
}

// quoteSummarySentence returns a sanitized, length-capped sentence ending in a period.
func quoteSummarySentence(s string) string {
	s = sanitizeInline(s)
	runes := []rune(s)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	out := strings.TrimSpace(string(runes))
	if out == "" {
		return ""
	}
	if !strings.HasSuffix(out, ".") {
		out += "."
	}
	return out
}

// sanitizeInline collapses whitespace and removes quotes/backticks/angle brackets so
// the derived description carries no markup.
func sanitizeInline(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '`' || r == '<' || r == '>':
			return ' '
		case r < 0x20:
			return ' '
		default:
			return r
		}
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// publicDocSummary returns the bounded description to attach to a search hit,
// defensively clamping legacy over-long descriptions (§4.5).
func publicDocSummary(tree *Tree) string {
	if tree == nil {
		return ""
	}
	return files.ClampSummaryText(tree.DocDescription, docSummaryMaxWords, docSummaryMaxBytes)
}
