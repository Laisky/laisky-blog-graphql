package pageindex

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// minDuration runs fn several times and returns the fastest run, which is the
// least noisy estimate of its cost on a shared machine. Each run starts after a
// garbage collection so earlier allocations are not billed to it.
func minDuration(runs int, fn func()) time.Duration {
	best := time.Duration(0)
	for i := 0; i < runs; i++ {
		runtime.GC()
		start := time.Now()
		fn()
		elapsed := time.Since(start)
		if i == 0 || elapsed < best {
			best = elapsed
		}
	}
	return best
}

// denseHeadingMarkdown returns n consecutive one-line ATX headings.
func denseHeadingMarkdown(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "# heading %d\n", i)
	}
	return []byte(b.String())
}

// plainProse returns about size bytes of ordinary paragraphs, the baseline a
// structural parser must stay proportional to.
func plainProse(size int) []byte {
	const paragraph = "The quick brown fox jumps over the lazy dog while the operator reviews the index budget.\n\n"
	return []byte(strings.Repeat(paragraph, size/len(paragraph)+1)[:size])
}

// TestExtractHeadersDenseHeadingsScaleLinearly reproduces issue #91: a document
// with a heading on every line made heading extraction quadratic because each
// heading recounted every newline before it. Quadrupling the heading count must
// cost roughly four times as much, not sixteen.
func TestExtractHeadersDenseHeadingsScaleLinearly(t *testing.T) {
	for name, shape := range map[string]struct {
		line     string
		small    int
		fallback bool
	}{
		// Under the block bound goldmark parses the document; over it the linear
		// scanner does. Both must scale linearly.
		"plain headings via goldmark":        {line: "# heading %d\n", small: 9_000},
		"inline headings via goldmark":       {line: "# *a_ [b](<c %d\n", small: 2_500},
		"plain headings via linear scanner":  {line: "# heading %d\n", small: 50_000, fallback: true},
		"inline headings via linear scanner": {line: "# *a_ [b](<c %d\n", small: 50_000, fallback: true},
	} {
		t.Run(name, func(t *testing.T) {
			line := shape.line
			build := func(n int) []byte {
				var b strings.Builder
				for i := 0; i < n; i++ {
					fmt.Fprintf(&b, line, i)
				}
				return []byte(b.String())
			}
			small, large := build(shape.small), build(4*shape.small)
			for _, body := range [][]byte{small, large} {
				extracted, err := extractMarkdownHeadings(t.Context(), body, 1)
				require.NoError(t, err)
				require.Equal(t, shape.fallback, extracted.fallback, extracted.fallbackReason)
			}
			var smallHeaders, largeHeaders []MDHeader
			smallCost := minDuration(5, func() {
				headers, err := ExtractHeaders(small)
				require.NoError(t, err)
				smallHeaders = headers
			})
			largeCost := minDuration(5, func() {
				headers, err := ExtractHeaders(large)
				require.NoError(t, err)
				largeHeaders = headers
			})
			require.Len(t, smallHeaders, shape.small)
			require.Len(t, largeHeaders, 4*shape.small)
			require.Equal(t, 4*shape.small, largeHeaders[len(largeHeaders)-1].LineNum, "line numbers must stay exact")

			ratio := float64(largeCost) / float64(smallCost)
			t.Logf("%d headings: %v, %d headings: %v, ratio %.1f", shape.small, smallCost, 4*shape.small, largeCost, ratio)
			require.Less(t, ratio, 8.0, "4x the headings must cost about 4x, not 16x (quadratic)")
		})
	}
}

// TestExtractHeadersPathologicalMarkdownCostsLikePlainText sweeps the same
// defect class through the Markdown parser itself: inputs that drive goldmark's
// inline and block parsers super-linear must cost about as much as ordinary
// prose of the same size, because only headings feed the tree.
func TestExtractHeadersPathologicalMarkdownCostsLikePlainText(t *testing.T) {
	const size = 256 << 10
	repeatTo := func(unit string) []byte { return []byte(strings.Repeat(unit, size/len(unit))) }
	cases := map[string][]byte{
		"mismatched emphasis":     repeatTo("*a_ "),
		"unclosed links":          repeatTo("[a](<b"),
		"nested brackets":         []byte(strings.Repeat("[", size/2) + "a" + strings.Repeat("]", size/2)),
		"consecutive link refs":   repeatTo("[a]: b\n"),
		"multi-line link refs":    repeatTo("[a]:\n b\n"),
		"nested block quotes":     repeatTo(">"),
		"nested list markers":     repeatTo("- "),
		"quoted list markers":     repeatTo("> - "),
		"heading with emphasis":   append([]byte("# "), repeatTo("*a_ ")...),
		"setext heading emphasis": append(repeatTo("*a_ "), []byte("\n===\n")...),
	}

	baseline := plainProse(size)
	baseCost := minDuration(3, func() {
		_, err := ExtractHeaders(baseline)
		require.NoError(t, err)
	})
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cost := minDuration(3, func() {
				_, err := ExtractHeaders(body)
				require.NoError(t, err)
			})
			t.Logf("%s: %v (baseline %v)", name, cost, baseCost)
			require.LessOrEqual(t, cost, 20*baseCost+100*time.Millisecond,
				"pathological Markdown must cost about as much as plain text of the same size")
		})
	}
}

// TestIndexMarkdownHonorsCanceledContext reproduces the missing cancellation
// in synchronous Markdown indexing: a caller that has gone away must not keep a
// worker parsing a large document.
func TestIndexMarkdownHonorsCanceledContext(t *testing.T) {
	idx := newMarkdownTestIndexer(t, defaultTestSettings(), NewStubLLM())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := idx.Index(ctx, KindMarkdown, denseHeadingMarkdown(50_000), IndexOptions{DocID: "canceled"}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

// countdownContext reports cancellation only after its Err method has been
// polled a fixed number of times, which cancels deterministically mid-walk.
type countdownContext struct {
	context.Context
	polls     atomic.Int64
	remaining int64
}

// Err returns context.Canceled once remaining polls have been used up.
func (c *countdownContext) Err() error {
	if c.polls.Add(1) > c.remaining {
		return context.Canceled
	}
	return nil
}

// TestIndexMarkdownStopsWhenCanceledMidParse cancels after indexing started
// and requires the heading walk to notice it instead of finishing the document.
func TestIndexMarkdownStopsWhenCanceledMidParse(t *testing.T) {
	idx := newMarkdownTestIndexer(t, defaultTestSettings(), NewStubLLM())
	// The run polls a handful of times between phases; ten polls can only be
	// reached by the periodic polls inside the line scan and the AST walk.
	ctx := &countdownContext{Context: context.Background(), remaining: 10}

	_, _, err := idx.Index(ctx, KindMarkdown, denseHeadingMarkdown(200_000), IndexOptions{DocID: "mid-parse"}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Greater(t, ctx.polls.Load(), int64(10), "parsing must poll the context while it runs, not only between phases")
}

// TestIndexMarkdownEmptyHeadingKeepsTreeValid reproduces a crash found while
// sweeping the parser: an empty ATX heading has no content segment, so its line
// was reported as 1 and a later section slice ran backwards and panicked.
func TestIndexMarkdownEmptyHeadingKeepsTreeValid(t *testing.T) {
	idx := newMarkdownTestIndexer(t, defaultTestSettings(), NewStubLLM())
	body := []byte("intro\n\n# A\n#\ntext\n\n##   \n\nmore\n")

	var tree *Tree
	require.NotPanics(t, func() {
		var err error
		tree, _, err = idx.Index(context.Background(), KindMarkdown, body, IndexOptions{DocID: "empty-heading"}, nil)
		require.NoError(t, err)
	})
	headers, err := ExtractHeaders(body)
	require.NoError(t, err)
	require.Equal(t, []MDHeader{{Level: 1, Title: "A", LineNum: 3}, {Level: 1, Title: "", LineNum: 4}, {Level: 2, Title: "", LineNum: 7}}, headers)
	require.Len(t, tree.Structure, 2)
	require.Equal(t, "# A", tree.Structure[0].Text)
}

// TestTokenizerCountBoundsLongRuns covers the same defect class in the token
// counter that the PDF pipeline already applies to untrusted page text: byte
// pair encoding is quadratic in the length of one pre-tokenized piece, so a
// long run without separators must cost about as much as ordinary prose.
func TestTokenizerCountBoundsLongRuns(t *testing.T) {
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	const size = 64 << 10
	baseline := string(plainProse(size))
	baseCost := minDuration(3, func() { _ = tok.Count(baseline) })
	for name, text := range map[string]string{
		"letters":    strings.Repeat("ab", size/2),
		"cjk":        strings.Repeat("中", size/3),
		"whitespace": strings.Repeat(" ", size),
		"symbols":    strings.Repeat("*_", size/2),
	} {
		t.Run(name, func(t *testing.T) {
			var count int
			cost := minDuration(3, func() { count = tok.Count(text) })
			t.Logf("%s: %v for %d tokens (baseline %v)", name, cost, count, baseCost)
			require.Positive(t, count)
			require.LessOrEqual(t, cost, 20*baseCost+100*time.Millisecond)
		})
	}
}

// newMarkdownTestIndexer builds an indexer around llm with the real tokenizer.
func newMarkdownTestIndexer(t *testing.T, cfg Settings, llm LLM) *Indexer {
	t.Helper()
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	return idx
}
