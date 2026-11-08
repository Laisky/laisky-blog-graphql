package pageindex

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MDHeader is one markdown header and its 1-indexed line number.
type MDHeader struct {
	Level   int
	Title   string
	LineNum int
}

// ExtractHeaders walks goldmark's AST and returns headings in document order.
// Its cost is linear in len(content); see extractMarkdownHeadings.
func ExtractHeaders(content []byte) ([]MDHeader, error) {
	extracted, err := extractMarkdownHeadings(context.Background(), content, 0)
	if err != nil {
		return nil, err
	}
	return extracted.headers, nil
}

// markdownHeadings is the result of one bounded heading extraction.
type markdownHeadings struct {
	headers []MDHeader
	// truncated reports that the document has more headings than were returned.
	truncated bool
	// fallback reports that the structural limits sent the document to the
	// linear ATX scanner instead of goldmark.
	fallback bool
}

// extractMarkdownHeadings returns up to maxHeaders headings (all when maxHeaders
// is not positive) with exact 1-indexed line numbers, polling ctx while it runs.
//
// Only headings feed the PageIndex tree, so goldmark builds the complete block
// structure but runs its inline parsers only inside headings (see
// newHeadingOnlyParser); that removes the super-linear emphasis and link
// processing of ordinary paragraphs while keeping every heading and title the
// full parser would produce. Line numbers come from one newline index searched
// by binary search instead of recounting the document prefix per heading. A
// document whose container nesting or link-reference layout would still drive
// goldmark's block parser super-linear is scanned by scanATXHeadings instead.
func extractMarkdownHeadings(ctx context.Context, content []byte, maxHeaders int) (markdownHeadings, error) {
	if err := ctx.Err(); err != nil {
		return markdownHeadings{}, errors.WithStack(err)
	}
	exceeds, err := markdownExceedsStructuralLimits(ctx, content)
	if err != nil {
		return markdownHeadings{}, err
	}
	if exceeds {
		return scanATXHeadings(ctx, content, maxHeaders)
	}

	root := newHeadingOnlyParser().Parse(text.NewReader(content))
	if err := ctx.Err(); err != nil {
		return markdownHeadings{}, errors.WithStack(err)
	}
	lines := newLineIndex(content)
	out := markdownHeadings{headers: make([]MDHeader, 0)}
	visited := 0
	walkErr := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		visited++
		if visited%markdownPollEvery == 0 {
			if err := ctx.Err(); err != nil {
				return ast.WalkStop, errors.WithStack(err)
			}
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		if maxHeaders > 0 && len(out.headers) == maxHeaders {
			out.truncated = true
			return ast.WalkStop, nil
		}
		title := clampHeadingTitle(strings.TrimSpace(headingText(h, content)))
		out.headers = append(out.headers, MDHeader{Level: h.Level, Title: title, LineNum: lines.line(headingOffset(h))})
		return ast.WalkSkipChildren, nil
	})
	if walkErr != nil {
		return markdownHeadings{}, errors.Wrap(walkErr, "walk markdown ast")
	}
	return out, nil
}

// ExtractText returns the markdown content stripped of formatting.
func ExtractText(content []byte) (string, error) {
	md := goldmark.New()
	root := md.Parser().Parse(text.NewReader(content))
	var buf bytes.Buffer
	source := content
	err := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			buf.Write(t.Segment.Value(source))
		case *ast.String:
			buf.Write(t.Value)
		case *ast.FencedCodeBlock:
			for i := 0; i < t.Lines().Len(); i++ {
				seg := t.Lines().At(i)
				buf.Write(seg.Value(source))
			}
		case *ast.CodeBlock:
			for i := 0; i < t.Lines().Len(); i++ {
				seg := t.Lines().At(i)
				buf.Write(seg.Value(source))
			}
		}
		switch n.Kind() {
		case ast.KindParagraph, ast.KindHeading, ast.KindCodeBlock, ast.KindFencedCodeBlock:
			if !entering {
				return ast.WalkContinue, nil
			}
			buf.WriteByte('\n')
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", errors.Wrap(err, "walk markdown ast")
	}
	return buf.String(), nil
}

// BuildMarkdownTree promotes a flat header list into the same Tree shape used for PDFs.
// Each node's text runs from its heading line to the line before the next heading;
// out-of-order or out-of-range line numbers yield an empty or clamped section
// instead of a panic.
func BuildMarkdownTree(headers []MDHeader, lines []string) []*Node {
	if len(headers) == 0 {
		return nil
	}
	type stackEntry struct {
		node  *Node
		level int
	}
	var roots []*Node
	var stack []stackEntry
	for i, h := range headers {
		start := min(max(h.LineNum-1, 0), len(lines))
		end := len(lines)
		if i+1 < len(headers) {
			end = headers[i+1].LineNum - 1
		}
		end = min(max(end, start), len(lines))
		node := &Node{Title: h.Title, LineNum: h.LineNum, Text: strings.Join(lines[start:end], "\n")}
		for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, node)
		} else {
			parent := stack[len(stack)-1].node
			parent.Children = append(parent.Children, node)
		}
		stack = append(stack, stackEntry{node: node, level: h.Level})
	}
	return roots
}

// headingOffset returns the byte offset of h's first line. goldmark records the
// block start for every heading, including an empty ATX heading that has no
// content segment; older trees used the first content segment, which lies on
// the same line whenever it exists.
func headingOffset(h *ast.Heading) int {
	if pos := h.Pos(); pos >= 0 {
		return pos
	}
	if h.Lines().Len() > 0 {
		return h.Lines().At(0).Start
	}
	return 0
}

// lineIndex holds the byte offset of every newline in a document, in order.
type lineIndex []int

// newLineIndex records every newline offset of src in one pass.
func newLineIndex(src []byte) lineIndex {
	idx := make(lineIndex, 0, bytes.Count(src, []byte{'\n'}))
	for off := 0; ; {
		next := bytes.IndexByte(src[off:], '\n')
		if next < 0 {
			return idx
		}
		off += next
		idx = append(idx, off)
		off++
	}
}

// line returns the 1-indexed line containing byte offset off: one more than the
// number of newlines before off, found by binary search.
func (l lineIndex) line(off int) int {
	if off <= 0 {
		return 1
	}
	return sort.SearchInts(l, off) + 1
}

// clampHeadingTitle caps a heading title at maxHeadingTitleBytes on a rune boundary.
func clampHeadingTitle(title string) string {
	if len(title) <= maxHeadingTitleBytes {
		return title
	}
	cut := maxHeadingTitleBytes
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return strings.TrimSpace(title[:cut])
}

// headingText collects visible inline text in the same order as the heading AST.
func headingText(node ast.Node, source []byte) string {
	var out strings.Builder
	var visit func(ast.Node)
	visit = func(n ast.Node) {
		switch v := n.(type) {
		case *ast.Text:
			out.Write(v.Segment.Value(source))
			if v.SoftLineBreak() {
				out.WriteByte('\n')
			}
		case *ast.String:
			out.Write(v.Value)
		default:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				visit(child)
			}
		}
	}
	visit(node)
	return out.String()
}
