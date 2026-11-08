package pageindex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// referenceHeaders is the extractor shipped before the structural budget: a full
// goldmark parse and a per-heading prefix newline count. It is the compatibility
// oracle for headings that have a content segment.
func referenceHeaders(t *testing.T, content []byte) []MDHeader {
	t.Helper()
	root := goldmark.New().Parser().Parse(text.NewReader(content))
	headers := make([]MDHeader, 0)
	require.NoError(t, ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		off := 0
		if h.Lines().Len() > 0 {
			seg := h.Lines().At(0)
			off = seg.Start
		}
		headers = append(headers, MDHeader{Level: h.Level, Title: strings.TrimSpace(headingText(h, content)), LineNum: bytes.Count(content[:off], []byte("\n")) + 1})
		return ast.WalkSkipChildren, nil
	}))
	return headers
}

// markdownCorpus returns the repository's own Markdown documents plus edge cases
// that exercise inline markup, containers and setext headings.
func markdownCorpus(t *testing.T) map[string][]byte {
	t.Helper()
	corpus := map[string][]byte{
		"inline markup":   []byte("# A *b* __c__ `d` [e](f) ![g](h) <i@j.k> <span>l</span> \\*m\\* &amp; n\n"),
		"reference link":  []byte("# See [docs][ref] and [ref]\n\ntext\n\n[ref]: https://example.com \"title\"\n"),
		"setext":          []byte("Title line one\nline *two*\n===\n\nSub\n---\n"),
		"containers":      []byte("> # quoted *h*\n>\n> - ## nested [l](u)\n\n1. ### ordered\n\n- item\n\n  # indented heading\n"),
		"code and html":   []byte("```\n# not a heading\n```\n\n<div>\n# raw\n</div>\n\n    # indented code\n\n# real\n"),
		"crlf":            []byte("# one\r\n\r\ntext\r\n\r\n## two\r\n"),
		"closing hashes":  []byte("## title ##\n### also # kept #\n#hashtag\n####### seven\n"),
		"ref definitions": []byte("[a]: /x\n[b]: /y\nSetext after refs\n===\n# [a] and [b]\n"),
	}
	root := filepath.Join("..", "..", "..", "..", "..", "docs")
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		corpus[path] = body
		return nil
	}))
	body, err := os.ReadFile(filepath.Join("testdata", "sample.md"))
	require.NoError(t, err)
	corpus["testdata/sample.md"] = body
	return corpus
}

// TestExtractHeadersMatchesFullGoldmarkParse proves the bounded extractor keeps
// every heading, title and line number of the previous full parse on real and
// edge-case documents, so existing trees are rebuilt unchanged.
func TestExtractHeadersMatchesFullGoldmarkParse(t *testing.T) {
	corpus := markdownCorpus(t)
	require.Greater(t, len(corpus), 20)
	for name, body := range corpus {
		got, err := ExtractHeaders(body)
		require.NoError(t, err, name)
		require.Equal(t, referenceHeaders(t, body), got, name)
	}
}

// TestLineIndexMatchesPrefixCount checks the binary-searched newline index
// against the old per-offset prefix count at every offset.
func TestLineIndexMatchesPrefixCount(t *testing.T) {
	for _, src := range []string{"", "a", "\n", "\n\n", "a\nb", "a\nb\n", "\r\n\r\nx\n", "abc\n\ndef\nghi\n\n"} {
		idx := newLineIndex([]byte(src))
		for off := 0; off <= len(src); off++ {
			require.Equal(t, strings.Count(src[:off], "\n")+1, idx.line(off), "src %q off %d", src, off)
		}
	}
}

// TestScanATXHeadingsFallback covers the linear fallback used for documents
// outside the structural limits: it still finds ATX headings and their lines,
// skips fenced code, and stops at the heading cap.
func TestScanATXHeadingsFallback(t *testing.T) {
	body := []byte(strings.Repeat("> ", 40) + "quoted\n# One #\n```\n# fenced\n```\n> ## Two\n- ### Three ###\n#not\n")
	extracted, err := extractMarkdownHeadings(t.Context(), body, 0)
	require.NoError(t, err)
	require.True(t, extracted.fallback)
	require.Equal(t, []MDHeader{{Level: 1, Title: "One", LineNum: 2}, {Level: 2, Title: "Two", LineNum: 6}, {Level: 3, Title: "Three", LineNum: 7}}, extracted.headers)

	capped, err := extractMarkdownHeadings(t.Context(), body, 2)
	require.NoError(t, err)
	require.True(t, capped.truncated)
	require.Len(t, capped.headers, 2)
}

// TestExtractHeadingsCapsHeadingCount covers the structural heading budget on the
// goldmark path: extraction stops at the cap and reports the truncation.
func TestExtractHeadingsCapsHeadingCount(t *testing.T) {
	extracted, err := extractMarkdownHeadings(t.Context(), denseHeadingMarkdown(5000), 100)
	require.NoError(t, err)
	require.False(t, extracted.fallback)
	require.True(t, extracted.truncated)
	require.Len(t, extracted.headers, 100)
	require.Equal(t, 100, extracted.headers[99].LineNum)
}

// TestLongHeadingIsPlainTextAndClamped covers the per-heading inline bound: a
// heading source over maxHeadingInlineBytes keeps its raw text and the stored
// title is clamped on a rune boundary.
func TestLongHeadingIsPlainTextAndClamped(t *testing.T) {
	long := "# " + strings.Repeat("*中_ ", 400) + "\n"
	headers, err := ExtractHeaders([]byte(long))
	require.NoError(t, err)
	require.Len(t, headers, 1)
	require.LessOrEqual(t, len(headers[0].Title), maxHeadingTitleBytes)
	require.True(t, strings.HasPrefix(headers[0].Title, "*中_"), "markup stays literal past the inline bound")
	require.True(t, utf8ValidString(headers[0].Title))
}

// utf8ValidString reports whether s is valid UTF-8.
func utf8ValidString(s string) bool { return strings.ToValidUTF8(s, "�") == s }
