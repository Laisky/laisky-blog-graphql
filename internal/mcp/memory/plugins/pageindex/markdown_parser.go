package pageindex

import (
	"bytes"
	"strings"

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
func ExtractHeaders(content []byte) ([]MDHeader, error) {
	md := goldmark.New()
	reader := text.NewReader(content)
	root := md.Parser().Parse(reader)
	headers := make([]MDHeader, 0)
	source := content
	walkErr := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		title := strings.TrimSpace(string(h.Text(source)))
		line := lineForOffset(source, segmentStart(h, source))
		headers = append(headers, MDHeader{Level: h.Level, Title: title, LineNum: line})
		return ast.WalkSkipChildren, nil
	})
	if walkErr != nil {
		return nil, errors.Wrap(walkErr, "walk markdown ast")
	}
	return headers, nil
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
		end := len(lines)
		if i+1 < len(headers) {
			end = headers[i+1].LineNum - 1
		}
		var body string
		if h.LineNum-1 < len(lines) {
			body = strings.Join(lines[h.LineNum-1:end], "\n")
		}
		node := &Node{Title: h.Title, LineNum: h.LineNum, Text: body}
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

func segmentStart(n ast.Node, source []byte) int {
	if hb := n.(*ast.Heading); hb.Lines().Len() > 0 {
		seg := hb.Lines().At(0)
		return seg.Start
	}
	_ = source
	return 0
}

func lineForOffset(source []byte, off int) int {
	if off > len(source) {
		off = len(source)
	}
	if off <= 0 {
		return 1
	}
	return bytes.Count(source[:off], []byte("\n")) + 1
}
