package pageindex

import (
	"bytes"
	"context"

	errors "github.com/Laisky/errors/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Structural bounds of the Markdown heading extractor. They are fixed service-side
// values: none of them can be raised by the caller, and an ordinary document never
// reaches them.
const (
	// markdownPollEvery is how many AST nodes or source lines are processed between
	// context polls.
	markdownPollEvery = 1024
	// maxHeadingInlineBytes is the largest heading source that is inline-parsed.
	// goldmark's emphasis and link processing is super-linear in the length of one
	// block, so a longer heading keeps its raw text as the title.
	maxHeadingInlineBytes = 1024
	// maxHeadingTitleBytes caps one stored heading title.
	maxHeadingTitleBytes = 1024
	// maxMarkdownContainerMarkers caps block-quote and list markers opened by one line.
	// goldmark's block parser is quadratic in the nesting opened by a single line.
	maxMarkdownContainerMarkers = 32
	// maxMarkdownContainerPrefixBytes caps the container prefix of a list item line.
	// List nesting needs at least two columns of indentation per level, so this
	// bounds the depth a long run of list lines can reach.
	maxMarkdownContainerPrefixBytes = 256
	// maxMarkdownLinkRefWork bounds the estimated link-reference work: for every run
	// of paragraph lines, the lines starting with '[' times the run length. goldmark
	// copies the remaining paragraph lines once per extracted definition.
	maxMarkdownLinkRefWork = 1 << 24
)

// newHeadingOnlyParser returns a parser with goldmark's default block parsers and
// paragraph transformers, so the block structure (and therefore the set of
// headings) is exactly that of goldmark.New(), but whose inline parsers run only
// inside headings of at most maxHeadingInlineBytes. A heading's inline content is
// parsed exactly as before, so titles are unchanged for ordinary documents.
func newHeadingOnlyParser() parser.Parser {
	gate := &headingInlineGate{}
	defaults := parser.DefaultInlineParsers()
	inline := make([]util.PrioritizedValue, 0, len(defaults))
	for _, v := range defaults {
		inner, ok := v.Value.(parser.InlineParser)
		if !ok {
			continue
		}
		wrapped := headingOnlyInlineParser{inner: inner, gate: gate}
		if closer, ok := inner.(parser.CloseBlocker); ok {
			inline = append(inline, util.Prioritized(headingOnlyCloseBlocker{headingOnlyInlineParser: wrapped, closer: closer}, v.Priority))
			continue
		}
		inline = append(inline, util.Prioritized(wrapped, v.Priority))
	}
	return parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(inline...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
}

// headingInlineGate decides, once per block, whether inline parsing may run.
// One gate belongs to one parser, which parses one document at a time.
type headingInlineGate struct {
	block   ast.Node
	allowed bool
}

// allow reports whether block is a heading whose source fits maxHeadingInlineBytes.
func (g *headingInlineGate) allow(block ast.Node) bool {
	if block == g.block {
		return g.allowed
	}
	g.block = block
	g.allowed = false
	heading, ok := block.(*ast.Heading)
	if !ok {
		return false
	}
	size := 0
	lines := heading.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		size += seg.Len()
		if size > maxHeadingInlineBytes {
			return false
		}
	}
	g.allowed = true
	return true
}

// headingOnlyInlineParser delegates to a goldmark inline parser inside allowed
// headings. Elsewhere it declines, so the trigger byte stays literal text.
type headingOnlyInlineParser struct {
	inner parser.InlineParser
	gate  *headingInlineGate
}

// Trigger returns the wrapped parser's trigger bytes.
func (p headingOnlyInlineParser) Trigger() []byte { return p.inner.Trigger() }

// Parse parses an inline node only inside an allowed heading.
func (p headingOnlyInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if !p.gate.allow(parent) {
		return nil
	}
	return p.inner.Parse(parent, block, pc)
}

// headingOnlyCloseBlocker keeps the wrapped parser's end-of-block hook, which only
// resolves state the parser itself pushed.
type headingOnlyCloseBlocker struct {
	headingOnlyInlineParser
	closer parser.CloseBlocker
}

// CloseBlock forwards the end-of-block hook to the wrapped parser.
func (p headingOnlyCloseBlocker) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	p.closer.CloseBlock(parent, block, pc)
}

// forEachLine calls fn with every line of content (without its newline) and its
// 1-indexed number, polling ctx every markdownPollEvery lines. fn may stop the
// scan by returning false.
func forEachLine(ctx context.Context, content []byte, fn func(line []byte, number int) bool) error {
	for number, off := 1, 0; off <= len(content); number++ {
		if number%markdownPollEvery == 0 {
			if err := ctx.Err(); err != nil {
				return errors.WithStack(err)
			}
		}
		end := bytes.IndexByte(content[off:], '\n')
		if end < 0 {
			end = len(content) - off
		}
		if !fn(content[off:off+end], number) {
			return nil
		}
		off += end + 1
	}
	return nil
}

// markdownExceedsStructuralLimits reports, in one linear pass, whether content
// has the container nesting or link-reference layout that drives goldmark's
// block parser super-linear. Ordinary documents never trip it.
func markdownExceedsStructuralLimits(ctx context.Context, content []byte) (bool, error) {
	exceeds := false
	linkRefWork, runLines, refLines := 0, 0, 0
	closeRun := func() {
		linkRefWork += runLines * refLines
		runLines, refLines = 0, 0
	}
	err := forEachLine(ctx, content, func(line []byte, _ int) bool {
		prefix := scanContainerPrefix(line)
		if prefix.markers > maxMarkdownContainerMarkers || (prefix.listItem && prefix.width > maxMarkdownContainerPrefixBytes) {
			exceeds = true
			return false
		}
		rest := line[prefix.width:]
		blank := len(bytes.TrimSpace(rest)) == 0
		// A blank line ends a paragraph and a list marker starts a new one.
		if blank || prefix.listItem {
			closeRun()
		}
		if blank {
			return true
		}
		runLines++
		if rest[0] == '[' {
			refLines++
		}
		if linkRefWork+runLines*refLines > maxMarkdownLinkRefWork {
			exceeds = true
			return false
		}
		return true
	})
	if err != nil {
		return false, err
	}
	return exceeds, nil
}

// containerPrefix describes the block-quote and list markers that open a line.
type containerPrefix struct {
	// width is the byte length of the markers and the whitespace around them.
	width int
	// markers counts block-quote and list markers.
	markers int
	// listItem reports that at least one marker is a list marker.
	listItem bool
}

// scanContainerPrefix measures the leading block-quote and list markers of line.
func scanContainerPrefix(line []byte) containerPrefix {
	var p containerPrefix
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '>':
			p.markers++
			i++
			continue
		case (c == '-' || c == '*' || c == '+') && i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t'):
			p.markers++
			p.listItem = true
			i += 2
			continue
		case c >= '0' && c <= '9':
			j := i
			for j < len(line) && j-i < 9 && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			if j+1 < len(line) && (line[j] == '.' || line[j] == ')') && (line[j+1] == ' ' || line[j+1] == '\t') {
				p.markers++
				p.listItem = true
				i = j + 2
				continue
			}
		}
		break
	}
	p.width = i
	return p
}

// scanATXHeadings is the linear fallback for documents outside the structural
// limits. It recognizes ATX headings outside fenced code, after any container
// markers, and keeps their raw text as the title; it does not resolve inline
// markup or setext headings, so it only serves documents goldmark cannot parse
// within bounded work.
func scanATXHeadings(ctx context.Context, content []byte, maxHeaders int) (markdownHeadings, error) {
	out := markdownHeadings{headers: make([]MDHeader, 0), fallback: true}
	var fenceChar byte
	fenceLen := 0
	err := forEachLine(ctx, content, func(line []byte, number int) bool {
		rest := bytes.TrimLeft(line[scanContainerPrefix(line).width:], " \t")
		if char, n := fenceRun(rest); n >= 3 {
			switch {
			case fenceLen == 0:
				fenceChar, fenceLen = char, n
			case char == fenceChar && n >= fenceLen && len(bytes.TrimSpace(rest[n:])) == 0:
				fenceLen = 0
			}
			return true
		}
		if fenceLen > 0 {
			return true
		}
		level := 0
		for level < len(rest) && rest[level] == '#' {
			level++
		}
		if level == 0 || level > 6 || (level < len(rest) && rest[level] != ' ' && rest[level] != '\t' && rest[level] != '\r') {
			return true
		}
		if maxHeaders > 0 && len(out.headers) == maxHeaders {
			out.truncated = true
			return false
		}
		title := bytes.TrimSpace(rest[level:])
		if closing := bytes.TrimRight(title, "#"); len(closing) == 0 || closing[len(closing)-1] == ' ' || closing[len(closing)-1] == '\t' {
			title = bytes.TrimSpace(closing)
		}
		out.headers = append(out.headers, MDHeader{Level: level, Title: clampHeadingTitle(string(title)), LineNum: number})
		return true
	})
	if err != nil {
		return markdownHeadings{}, err
	}
	return out, nil
}

// fenceRun returns the fence character and run length opening line, if any.
func fenceRun(line []byte) (byte, int) {
	if len(line) == 0 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
}
