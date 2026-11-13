package pageindex

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// markdownCost returns the wall time and bytes allocated by one bounded extraction.
func markdownCost(t *testing.T, body []byte) (time.Duration, uint64, markdownHeadings) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	extracted, err := extractMarkdownHeadings(context.Background(), body, DefaultMaxNodesPerDocument)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	return elapsed, after.TotalAlloc - before.TotalAlloc, extracted
}

// listShapedDocuments returns the block-heavy 2 MB shapes from the review of #96
// plus documents just under each goldmark work bound.
func listShapedDocuments(size int) map[string][]byte {
	rep := func(unit string) []byte { return []byte(strings.Repeat(unit, size/len(unit))) }
	siblings := func(depth int, indent string) []byte {
		var b strings.Builder
		for d := 0; d < depth; d++ {
			b.WriteString(strings.Repeat(indent, d))
			b.WriteString("- x\n")
		}
		for b.Len() < size {
			b.WriteString(strings.Repeat(indent, depth-1) + "- x\n")
		}
		return []byte(b.String())
	}
	// pad fills to size with one long paragraph line, which adds no blocks.
	pad := func(head string) []byte {
		return []byte(head + strings.Repeat("word ", (size-len(head))/5) + "\n")
	}
	return map[string][]byte{
		"tab-indented list 254 deep":   siblings(254, "\t"),
		"space-indented list 128 deep": siblings(128, "  "),
		"flat list items":              rep("- a\n"),
		"empty star items":             rep("*\n"),
		"empty headings":               rep("#\n"),
		"items at depth 4":             siblings(4, "  "),
		"list then blank lines":        append([]byte("- a\n"), rep("\n")...),
		"one-char paragraphs":          rep("a\n\n"),
		"lazy block quote":             append([]byte(strings.Repeat("> ", 30)+"x\n"), rep("y\n")...),
		"1KiB headings of brackets":    rep("# " + strings.Repeat("[", 1021) + "\n"),
		"1KiB headings of emphasis":    rep("# " + strings.Repeat("*a_ ", 255) + "\n"),
		"just under the block bound":   pad(strings.Repeat("- a\n", maxMarkdownBlocks/2-1)),
		"just under the line bound":    pad("p\n" + strings.Repeat("a\n", maxMarkdownLines-2)),
	}
}

// TestListShapedMarkdownStaysBounded covers review item 2 of #96: list- and
// block-shaped Markdown made goldmark spend seconds and hundreds of megabytes per
// 2 MB write, because its Parse cannot be interrupted. Each shape must cost at
// most 20x the plain-prose baseline plus 100 ms and allocate at most 4x the
// baseline plus 32 MB. Timing is skipped under the race detector.
func TestListShapedMarkdownStaysBounded(t *testing.T) {
	const size = 2 << 20
	baseCost, baseAlloc, _ := markdownCost(t, plainProse(size))
	for name, body := range listShapedDocuments(size) {
		t.Run(name, func(t *testing.T) {
			cost, alloc, extracted := markdownCost(t, body)
			t.Logf("%s: %v %d MB fallback=%v %s (baseline %v %d MB)", name, cost, alloc>>20, extracted.fallback, extracted.fallbackReason, baseCost, baseAlloc>>20)
			require.LessOrEqual(t, alloc, 4*baseAlloc+(32<<20), "allocation must stay proportional to plain prose")
			if !raceEnabled {
				require.LessOrEqual(t, cost, 20*baseCost+100*time.Millisecond, "CPU must stay proportional to plain prose")
			}
		})
	}
}

// TestGoldmarkWorkBoundsRouteBeforeParsing checks which side of each bound a
// document lands on, so the pre-scan, not goldmark, decides before any parse.
func TestGoldmarkWorkBoundsRouteBeforeParsing(t *testing.T) {
	for name, tc := range map[string]struct {
		body   []byte
		reason string
	}{
		"tab-indented nesting is measured in columns": {[]byte("- a\n" + strings.Repeat("\t", 16) + "- x\n"), "nesting"},
		"the same depth with spaces stays":            {[]byte("- a\n" + strings.Repeat("  ", 16) + "- x\n"), ""},
		"indented markers outside a list are code":    {[]byte("text\n\n" + strings.Repeat("\t", 16) + "- x\n"), ""},
		"markers on an otherwise empty line count":    {[]byte(strings.Repeat("> ", 40) + "\n"), "nesting"},
		"blocks":                            {[]byte(strings.Repeat("- a\n", maxMarkdownBlocks)), "blocks"},
		"blank lines inside an open list":   {append([]byte("- a\n"), strings.Repeat("\n", maxMarkdownLines+1)...), "lines"},
		"blank lines at top level are free": {append([]byte("# a\n"), strings.Repeat("\n", 4*maxMarkdownLines)...), ""},
	} {
		assessment, err := assessMarkdownStructure(context.Background(), tc.body)
		require.NoError(t, err)
		require.Equal(t, tc.reason, assessment.reason, "%s: %+v", name, assessment)
	}
}

// TestOrdinaryDocumentsKeepGoldmark covers review item 4 of #96: a spaced
// thematic break, a pasted log inside a fence, or code samples sent ordinary
// documents to the fallback scanner and changed their titles and node IDs.
func TestOrdinaryDocumentsKeepGoldmark(t *testing.T) {
	var logs strings.Builder
	logs.WriteString("Setext Title\n============\n\n## Logs for `svc`\n\n```\n")
	for i := 0; i < 4200; i++ {
		fmt.Fprintf(&logs, "[2026-11-10T00:00:%02d] INFO request %d served\n", i%60, i)
	}
	logs.WriteString("```\n\n## After\n")
	var refs strings.Builder
	refs.WriteString("# References\n\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&refs, "[r%d]: https://example.com/%d\n", i, i)
	}
	refs.WriteString("\n## See [r1]\n")
	for name, doc := range map[string]string{
		"spaced thematic break": "Intro\n=====\n\nSome text.\n\n" + strings.Repeat("- ", 33) + "-\n\n## The `foo` function\n\nMore [link](http://x) text.\n\n```python\n    # not a heading\n```\n\n    # indented code, not a heading\n",
		"star thematic break":   "# Title *emph*\n\n" + strings.Repeat("* ", 40) + "\n\nSub\n---\n",
		"log lines in fence":    logs.String(),
		"deeply indented code":  "# Code\n\n```\n" + strings.Repeat(strings.Repeat(" ", 300)+"- item\n", 50) + "```\n\n" + strings.Repeat(" ", 8) + strings.Repeat("- ", 40) + "x\n",
		"many link references":  refs.String(),
	} {
		got, err := extractMarkdownHeadings(context.Background(), []byte(doc), 0)
		require.NoError(t, err)
		require.False(t, got.fallback, "%s: %s", name, got.fallbackReason)
		require.Equal(t, referenceHeaders(t, []byte(doc)), got.headers, name)
	}
}

// TestLinearScannerMatchesGoldmarkOnCorpus forces the linear fallback scanner over
// the repository's documents and edge cases: a document over a work bound keeps
// the headings, titles and lines goldmark would give it.
func TestLinearScannerMatchesGoldmarkOnCorpus(t *testing.T) {
	for name, body := range markdownCorpus(t) {
		got, err := scanMarkdownHeadingsLinear(context.Background(), body, 0)
		require.NoError(t, err)
		require.True(t, got.fallback)
		require.Equal(t, referenceHeaders(t, body), got.headers, name)
	}
}

// TestLinearScannerStopsAtHeadingCap checks that the fallback scanner stops at
// the heading cap instead of reading the rest of the document: with the cap
// reached in the first lines it never polls the (already canceled) context.
func TestLinearScannerStopsAtHeadingCap(t *testing.T) {
	ctx := &countdownContext{Context: context.Background(), remaining: 0}
	got, err := scanMarkdownHeadingsLinear(ctx, []byte(strings.Repeat("# h\n", 1_000_000)), 10)
	require.NoError(t, err)
	require.True(t, got.truncated)
	require.Len(t, got.headers, 10)
	require.Zero(t, ctx.polls.Load())
}
