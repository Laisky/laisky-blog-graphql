package pageindex

import (
	"strings"

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
	// maxHeadingInlineBytesTotal bounds the heading source inline-parsed in one
	// document. Inline work per heading grows faster than its length, so this keeps
	// the total bounded however many long headings a document has; later headings
	// keep their raw text.
	maxHeadingInlineBytesTotal = 256 << 10
	// maxHeadingInlineTriggersTotal bounds the inline markup bytes (emphasis, code,
	// link, image, raw HTML, entity and escape characters) inline-parsed in one
	// document; goldmark allocates parser state for each of them.
	maxHeadingInlineTriggersTotal = 32 << 10
	// maxHeadingTitleBytes caps one stored heading title.
	maxHeadingTitleBytes = 1024
)

// newHeadingOnlyParser returns a parser with goldmark's default block parsers and
// paragraph transformers, so the block structure (and therefore the set of
// headings) is exactly that of goldmark.New(), but whose inline parsers run only
// inside headings of at most maxHeadingInlineBytes. A heading's inline content is
// parsed exactly as before, so titles are unchanged for ordinary documents.
func newHeadingOnlyParser() parser.Parser {
	gate := &headingInlineGate{remaining: maxHeadingInlineBytesTotal, triggers: maxHeadingInlineTriggersTotal}
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
	// remaining is the heading source the document may still inline-parse, and
	// triggers the inline markup bytes it may still parse.
	remaining, triggers int
}

// allow reports whether block is a heading whose source fits maxHeadingInlineBytes
// and the document's remaining inline budgets, which it then consumes.
func (g *headingInlineGate) allow(block ast.Node, source []byte) bool {
	if block == g.block {
		return g.allowed
	}
	g.block = block
	g.allowed = false
	heading, ok := block.(*ast.Heading)
	if !ok {
		return false
	}
	size, triggers := 0, 0
	lines := heading.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		size += seg.Len()
		if size > maxHeadingInlineBytes || size > g.remaining {
			return false
		}
		triggers += countInlineTriggers(seg.Value(source))
	}
	if triggers > g.triggers {
		return false
	}
	g.remaining -= size
	g.triggers -= triggers
	g.allowed = true
	return true
}

// inlineTitleTriggers are the bytes that can start inline markup or an escape in
// a heading; a title without them renders as its own trimmed text.
const inlineTitleTriggers = "*_`[]!<>&\\"

// countInlineTriggers returns the number of inline markup bytes in b.
func countInlineTriggers(b []byte) int {
	count := 0
	for _, c := range b {
		if strings.IndexByte(inlineTitleTriggers, c) >= 0 {
			count++
		}
	}
	return count
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
	if !p.gate.allow(parent, block.Source()) {
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
