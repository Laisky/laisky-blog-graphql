package pageindex

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// FuzzMarkdownHeadingExtraction checks the invariants every heading extraction
// must keep on arbitrary input, on the goldmark path and on the linear fallback
// scanner alike: no error or panic, levels 1 to 6, line numbers inside the
// document and in order, titles within maxHeadingTitleBytes (and valid UTF-8 for
// valid input), at most the heading cap, and a tree that builds. Exact parity with
// goldmark is checked on the corpus by TestLinearScannerMatchesGoldmarkOnCorpus;
// the fallback approximates CommonMark, so this target checks only invariants.
// Run it deliberately: go test -run '^$' -fuzz FuzzMarkdownHeadingExtraction.
func FuzzMarkdownHeadingExtraction(f *testing.F) {
	for _, seed := range []string{
		"# A *b* __c__ `d` [e](f)\n",
		"# See [docs][ref] and [ref]\n\ntext\n\n[ref]: https://example.com \"title\"\n",
		"Title line one\nline *two*\n===\n\nSub\n---\n",
		"> # quoted *h*\n>\n> - ## nested [l](u)\n\n1. ### ordered\n\n- item\n\n  # indented heading\n",
		"```\n# not a heading\n```\n\n<div>\n# raw\n</div>\n\n    # indented code\n\n# real\n",
		"# one\r\n\r\ntext\r\n\r\n## two\r\n",
		"#\n\n##\n#######\n# #\n",
		"[a]: /x\n[b]: /y\nSetext after refs\n===\n# [a] and [b]\n",
		strings.Repeat("> ", 40) + "deep\n\n# after\n",
		"- a\n\t\t\t\t\t\t\t\t\t- b\n# h\n",
	} {
		f.Add([]byte(seed))
	}
	const maxHeaders = 64
	f.Fuzz(func(t *testing.T, body []byte) {
		lines := bytes.Count(body, []byte("\n")) + 1
		check := func(name string, got markdownHeadings) {
			require.LessOrEqual(t, len(got.headers), maxHeaders, name)
			last := 0
			for _, h := range got.headers {
				require.GreaterOrEqual(t, h.Level, 1, name)
				require.LessOrEqual(t, h.Level, 6, name)
				require.GreaterOrEqual(t, h.LineNum, max(last, 1), "%s: lines in order", name)
				require.LessOrEqual(t, h.LineNum, lines, name)
				require.LessOrEqual(t, len(h.Title), maxHeadingTitleBytes, name)
				if utf8.Valid(body) {
					require.True(t, utf8.ValidString(h.Title), "%s: title %q", name, h.Title)
				}
				last = h.LineNum
			}
			require.NotPanics(t, func() { BuildMarkdownTree(got.headers, strings.Split(string(body), "\n")) }, name)
		}
		got, err := extractMarkdownHeadings(context.Background(), body, maxHeaders)
		require.NoError(t, err)
		check("extract", got)
		linear, err := scanMarkdownHeadingsLinear(context.Background(), body, maxHeaders)
		require.NoError(t, err)
		check("linear", linear)
	})
}
