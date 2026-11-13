package pageindex

import (
	"bytes"
	"context"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Work bounds for goldmark. goldmark.Parser.Parse takes no context and cannot be
// interrupted, so a document is parsed by goldmark only when one linear pre-scan
// predicts bounded work; every other document is scanned by
// scanMarkdownHeadingsLinear. The values are fixed service-side bounds, measured so
// that a 2 MB document under them costs at most tens of milliseconds and tens of
// megabytes (docs/ref/pageindex_structural_budgets_20261108.md); ordinary documents
// of that size stay well below them.
const (
	// maxMarkdownNesting caps the estimated container depth of one line that opens a
	// container: block-quote and list markers plus half the indentation columns
	// (list nesting needs at least two columns per level). goldmark's work per line
	// grows with the depth it must continue through.
	maxMarkdownNesting = 32
	// maxMarkdownNestingWork caps the sum of estimated container depth over all lines.
	maxMarkdownNestingWork = 400_000
	// maxMarkdownBlocks caps the blocks goldmark would build (paragraphs, list items,
	// headings, thematic breaks, fences, HTML blocks, block quotes); each costs
	// hundreds of bytes of AST.
	maxMarkdownBlocks = 40_000
	// maxMarkdownLines caps the lines goldmark would keep as block content: every
	// non-blank line, and blank lines inside an open list.
	maxMarkdownLines = 80_000
	// maxMarkdownLinkRefWork bounds the estimated link-reference work: for every run
	// of paragraph lines, the lines starting with '[' times the run length. goldmark
	// copies the remaining paragraph lines once per extracted definition.
	maxMarkdownLinkRefWork = 1 << 24
	// markdownTabStop is the CommonMark tab stop used to measure indentation.
	markdownTabStop = 4
)

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

// containerPrefix describes the block-quote and list markers that open a line.
type containerPrefix struct {
	// width is the byte length of the markers and the whitespace around them.
	width int
	// quotes and lists count block-quote and list markers.
	quotes, lists int
	// leadCols is the indentation, in columns, before the first marker.
	leadCols int
	// indentCols is the indentation, in columns, between the last marker (or the
	// line start) and the content.
	indentCols int
}

// markers returns the number of container markers on the line.
func (p containerPrefix) markers() int { return p.quotes + p.lists }

// nesting estimates the container depth the line continues through or opens.
func (p containerPrefix) nesting() int { return p.markers() + (p.leadCols+p.indentCols)/2 }

// scanContainerPrefix measures the leading block-quote and list markers of line,
// counting indentation in columns with tabs advancing to the next tab stop.
func scanContainerPrefix(line []byte) containerPrefix {
	var p containerPrefix
	col, runStart := 0, 0
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ':
			col++
			i++
			continue
		case c == '\t':
			col += markdownTabStop - col%markdownTabStop
			i++
			continue
		case c == '>':
			p.noteMarker(col - runStart)
			p.quotes++
			col++
			i++
			runStart = col
			continue
		case (c == '-' || c == '*' || c == '+') && i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t'):
			p.noteMarker(col - runStart)
			p.lists++
			col++
			i++
			runStart = col
			continue
		case c >= '0' && c <= '9':
			j := i
			for j < len(line) && j-i < 9 && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			if j+1 < len(line) && (line[j] == '.' || line[j] == ')') && (line[j+1] == ' ' || line[j+1] == '\t') {
				p.noteMarker(col - runStart)
				p.lists++
				col += j + 1 - i
				i = j + 1
				runStart = col
				continue
			}
		}
		break
	}
	p.width = i
	p.indentCols = col - runStart
	if p.markers() == 0 {
		p.leadCols = 0
	}
	return p
}

// leadingColumns returns the indentation of line in columns, with tabs advancing
// to the next tab stop.
func leadingColumns(line []byte) int {
	col := 0
	for _, c := range line {
		switch c {
		case ' ':
			col++
		case '\t':
			col += markdownTabStop - col%markdownTabStop
		default:
			return col
		}
	}
	return col
}

// noteMarker records the indentation before the first marker of a line.
func (p *containerPrefix) noteMarker(gap int) {
	if p.markers() == 0 {
		p.leadCols = gap
	}
}

// lineKind classifies one Markdown line for the linear scanners.
type lineKind int

const (
	lineBlank lineKind = iota
	lineFence
	lineFenced
	lineIndentedCode
	lineATX
	lineSetext
	lineThematic
	lineHTML
	lineHTMLContent
	lineParagraphStart
	lineParagraphText
)

// markdownLine is one classified line.
type markdownLine struct {
	kind   lineKind
	prefix containerPrefix
	// rest is the line content after the container prefix.
	rest []byte
	// level is the heading level of an ATX or setext line.
	level int
}

// lineClassifier tracks the block context needed to classify lines in one pass:
// fenced code, indented code, the current paragraph and whether a list is open.
// It approximates CommonMark closely enough for heading extraction and cost
// prediction; it never builds a tree.
type lineClassifier struct {
	fenceChar byte
	fenceLen  int
	// paragraph reports that the previous line belongs to a paragraph.
	paragraph bool
	// paragraphCol is the content column of the line that opened the paragraph.
	paragraphCol int
	// paragraphInList reports that the paragraph opened on a list item line.
	paragraphInList bool
	// listOpen reports that a list item may still be open.
	listOpen bool
	// afterBlank reports that the previous line was blank.
	afterBlank bool
	// html reports an open HTML block; htmlEnd is its end marker, or nil when it
	// ends at a blank line.
	html    bool
	htmlEnd []byte
}

// next classifies line given the lines before it.
func (c *lineClassifier) next(line []byte) markdownLine {
	prefix := scanContainerPrefix(line)
	rest := line[prefix.width:]
	blank := len(bytes.TrimSpace(rest)) == 0
	if c.fenceLen > 0 {
		return c.fencedLine(prefix, rest)
	}
	out := markdownLine{prefix: prefix, rest: rest}
	defer func() { c.afterBlank = out.kind == lineBlank }()
	if c.html {
		out.kind = c.htmlLine(line, blank)
		return out
	}
	if isThematicBreak(line) && (!c.paragraph || !isSetextUnderline(rest) || prefix.lists != 0) {
		// "- - -" and "* * *" are breaks, not list markers.
		out.prefix.lists = 0
		c.paragraph = false
		out.kind = lineThematic
		return out
	}
	if blank {
		c.paragraph = false
		out.kind = lineBlank
		return out
	}
	// Four columns of indentation outside a paragraph and a list open indented
	// code, whatever follows, so markers inside it are text.
	if lead := leadingColumns(line); lead >= 4 && !c.paragraph && (prefix.markers() == 0 || !c.listOpen) && (prefix.quotes == 0 || prefix.leadCols >= 4) {
		out.prefix = containerPrefix{width: prefix.width, indentCols: lead}
		out.kind = lineIndentedCode
		return out
	}
	if prefix.markers() == 0 && prefix.indentCols == 0 && c.afterBlank {
		c.listOpen = false
	}
	if prefix.lists > 0 {
		c.listOpen = true
	}
	if prefix.indentCols < 4 {
		if kind, level, ok := c.openingLine(prefix, rest); ok {
			c.paragraph = false
			out.kind, out.level = kind, level
			return out
		}
	}
	if c.paragraph && prefix.lists == 0 {
		out.kind = lineParagraphText
		return out
	}
	c.paragraph = true
	c.paragraphInList = prefix.lists > 0
	c.paragraphCol = prefix.indentCols + prefix.markers()
	out.kind = lineParagraphStart
	return out
}

// fencedLine classifies a line inside an open fence.
func (c *lineClassifier) fencedLine(prefix containerPrefix, rest []byte) markdownLine {
	if char, n := fenceRun(rest); n >= c.fenceLen && char == c.fenceChar && prefix.indentCols < 4 && len(bytes.TrimSpace(rest[n:])) == 0 {
		c.fenceLen = 0
		return markdownLine{kind: lineFence, prefix: prefix, rest: rest}
	}
	return markdownLine{kind: lineFenced, prefix: prefix, rest: rest}
}

// htmlLine classifies a line inside an open HTML block and closes the block at
// its end marker or, for types 6 and 7, at a blank line.
func (c *lineClassifier) htmlLine(line []byte, blank bool) lineKind {
	switch {
	case c.htmlEnd == nil && blank:
		c.html = false
		return lineBlank
	case c.htmlEnd != nil && bytes.Contains(bytes.ToLower(line), c.htmlEnd):
		c.html = false
	}
	return lineHTMLContent
}

// openingLine recognizes a line, indented less than four columns, that opens a
// fence, an ATX or setext heading, or an HTML block, and updates the fence and
// HTML state.
func (c *lineClassifier) openingLine(prefix containerPrefix, rest []byte) (lineKind, int, bool) {
	if char, n := fenceRun(rest); n >= 3 && (char == '~' || bytes.IndexByte(rest[n:], '`') < 0) {
		c.fenceChar, c.fenceLen = char, n
		return lineFence, 0, true
	}
	if level := atxLevel(rest); level > 0 {
		return lineATX, level, true
	}
	if c.paragraph && prefix.lists == 0 && isSetextUnderline(rest) && (!c.paragraphInList || prefix.indentCols+prefix.markers() >= c.paragraphCol) {
		if bytes.TrimSpace(rest)[0] == '-' {
			return lineSetext, 2, true
		}
		return lineSetext, 1, true
	}
	if end, ok := htmlBlockStart(rest, c.paragraph && prefix.lists == 0); ok {
		c.html, c.htmlEnd = true, end
		if end != nil && bytes.Contains(bytes.ToLower(rest[1:]), end) {
			c.html = false
		}
		return lineHTML, 0, true
	}
	return 0, 0, false
}

// htmlBlockTags are the CommonMark type-6 HTML block tag names.
var htmlBlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "base": true, "basefont": true, "blockquote": true, "body": true,
	"caption": true, "center": true, "col": true, "colgroup": true, "dd": true, "details": true, "dialog": true, "dir": true,
	"div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true, "figure": true, "footer": true, "form": true,
	"frame": true, "frameset": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "head": true,
	"header": true, "hr": true, "html": true, "iframe": true, "legend": true, "li": true, "link": true, "main": true,
	"menu": true, "menuitem": true, "nav": true, "noframes": true, "ol": true, "optgroup": true, "option": true, "p": true,
	"param": true, "search": true, "section": true, "summary": true, "table": true, "tbody": true, "td": true, "tfoot": true,
	"th": true, "thead": true, "title": true, "tr": true, "track": true, "ul": true,
}

// htmlBlockStart reports whether rest opens a CommonMark HTML block and returns
// its lower-case end marker, or nil for a block that ends at a blank line.
// inParagraph excludes type 7, which cannot interrupt a paragraph.
func htmlBlockStart(rest []byte, inParagraph bool) ([]byte, bool) {
	if len(rest) < 2 || rest[0] != '<' {
		return nil, false
	}
	lower := bytes.ToLower(bytes.TrimRight(rest, " \t\r"))
	for _, raw := range []string{"pre", "script", "style", "textarea"} {
		if tag := "<" + raw; bytes.HasPrefix(lower, []byte(tag)) && (len(lower) == len(tag) || strings.IndexByte(" \t>", lower[len(tag)]) >= 0) {
			return []byte("</" + raw + ">"), true
		}
	}
	switch {
	case bytes.HasPrefix(lower, []byte("<!--")):
		return []byte("-->"), true
	case bytes.HasPrefix(lower, []byte("<?")):
		return []byte("?>"), true
	case bytes.HasPrefix(lower, []byte("<![cdata[")):
		return []byte("]]>"), true
	case len(lower) > 2 && lower[1] == '!' && lower[2] >= 'a' && lower[2] <= 'z':
		return []byte(">"), true
	}
	name := bytes.TrimPrefix(lower[1:], []byte("/"))
	end := 0
	for end < len(name) && (name[end] >= 'a' && name[end] <= 'z' || name[end] >= '0' && name[end] <= '9' || name[end] == '-') {
		end++
	}
	if end == 0 {
		return nil, false
	}
	if htmlBlockTags[string(name[:end])] && (end == len(name) || strings.IndexByte(" \t>/", name[end]) >= 0) {
		return nil, true
	}
	// Type 7: one complete open or closing tag alone on the line.
	if !inParagraph && lower[len(lower)-1] == '>' && bytes.IndexByte(lower[1:len(lower)-1], '<') < 0 {
		return nil, true
	}
	return nil, false
}

// isThematicBreak reports whether line, after block-quote markers, is three or
// more '-', '*' or '_' characters with optional spaces and tabs.
func isThematicBreak(line []byte) bool {
	rest := bytes.TrimLeft(line, " \t>")
	if len(rest) < 3 {
		return false
	}
	char := rest[0]
	if char != '-' && char != '*' && char != '_' {
		return false
	}
	count := 0
	for _, c := range rest {
		switch c {
		case char:
			count++
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return count >= 3
}

// isSetextUnderline reports whether rest is a run of '=' or '-' with optional
// trailing whitespace.
func isSetextUnderline(rest []byte) bool {
	trimmed := bytes.TrimRight(rest, " \t\r")
	if len(trimmed) == 0 || (trimmed[0] != '=' && trimmed[0] != '-') {
		return false
	}
	for _, c := range trimmed {
		if c != trimmed[0] {
			return false
		}
	}
	return true
}

// atxLevel returns the level of an ATX heading line, or 0.
func atxLevel(rest []byte) int {
	level := 0
	for level < len(rest) && rest[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || (level < len(rest) && rest[level] != ' ' && rest[level] != '\t' && rest[level] != '\r') {
		return 0
	}
	return level
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

// markdownAssessment is the work the pre-scan predicts for goldmark.
type markdownAssessment struct {
	lines, blocks, nestingWork, linkRefWork, maxNesting int
	// reason names the first bound exceeded, or is empty.
	reason string
}

// assessMarkdownStructure predicts, in one linear pass, whether goldmark would do
// bounded work on content. It returns the first exceeded bound in reason. Fenced
// and indented code, thematic breaks and headings are classified first, so code
// samples, logs and separators do not count as containers or link references.
func assessMarkdownStructure(ctx context.Context, content []byte) (markdownAssessment, error) {
	var a markdownAssessment
	var cls lineClassifier
	runLines, refLines, quoteDepth := 0, 0, 0
	closeRun := func() {
		a.linkRefWork += runLines * refLines
		runLines, refLines = 0, 0
	}
	exceed := func(reason string) bool {
		a.reason = reason
		return false
	}
	err := forEachLine(ctx, content, func(line []byte, _ int) bool {
		l := cls.next(line)
		switch l.kind {
		case lineBlank:
			closeRun()
			quoteDepth = 0
			if cls.listOpen {
				a.lines++
			}
		case lineFenced, lineIndentedCode, lineHTMLContent:
			a.lines++
		case lineFence, lineATX, lineSetext, lineThematic, lineHTML:
			closeRun()
			a.lines++
			a.blocks++
		case lineParagraphStart:
			closeRun()
			a.lines++
			a.blocks++
		case lineParagraphText:
			a.lines++
		}
		// Container markers open blocks even on a line with no other content.
		if l.kind != lineFenced && l.kind != lineIndentedCode && l.kind != lineHTMLContent {
			a.blocks += l.prefix.lists
			if l.prefix.quotes > quoteDepth {
				a.blocks += l.prefix.quotes - quoteDepth
			}
			quoteDepth = l.prefix.quotes
			nesting := l.prefix.nesting()
			a.nestingWork += nesting
			if l.prefix.markers() > 0 {
				a.maxNesting = max(a.maxNesting, nesting)
			}
		}
		if l.kind == lineParagraphStart || l.kind == lineParagraphText {
			runLines++
			if l.rest[0] == '[' {
				refLines++
			}
		}
		switch {
		case a.maxNesting > maxMarkdownNesting:
			return exceed("nesting")
		case a.nestingWork > maxMarkdownNestingWork:
			return exceed("nesting_work")
		case a.blocks > maxMarkdownBlocks:
			return exceed("blocks")
		case a.lines > maxMarkdownLines:
			return exceed("lines")
		case a.linkRefWork+runLines*refLines > maxMarkdownLinkRefWork:
			return exceed("link_references")
		}
		return true
	})
	if err != nil {
		return markdownAssessment{}, err
	}
	return a, nil
}

// scanMarkdownHeadingsLinear is the linear heading extractor for documents whose
// predicted goldmark work exceeds the bounds. It recognizes ATX and setext
// headings outside fenced and indented code, after container markers, and stops
// at maxHeaders (when positive). A title with inline markup is inline-parsed on
// its own (at most maxHeadingInlineBytes of source, without link-reference
// definitions), so emphasis, code spans and links render as goldmark renders them.
func scanMarkdownHeadingsLinear(ctx context.Context, content []byte, maxHeaders int) (markdownHeadings, error) {
	out := markdownHeadings{headers: make([]MDHeader, 0), fallback: true}
	type pending struct {
		raw    string
		setext bool
	}
	raws := make([]pending, 0)
	var cls lineClassifier
	var paragraph, defs []string
	paragraphLine, paragraphBytes, defBytes := 0, 0, 0
	// leadingDefs reports that every paragraph line so far is a link-reference
	// definition, which goldmark removes before it decides on a setext heading.
	leadingDefs := false
	add := func(level int, raw string, lineNum int, setext bool) bool {
		if maxHeaders > 0 && len(out.headers) == maxHeaders {
			out.truncated = true
			return false
		}
		out.headers = append(out.headers, MDHeader{Level: level, LineNum: lineNum})
		raws = append(raws, pending{raw: raw, setext: setext})
		return true
	}
	paragraphLineText := func(l markdownLine, number int) {
		text := string(bytes.TrimSpace(l.rest))
		if leadingDefs && isLinkRefDefinition(text) {
			if len(defs) < maxFallbackLinkRefs && defBytes+len(text) <= maxFallbackLinkRefBytes {
				defs = append(defs, text)
				defBytes += len(text)
			}
			return
		}
		if leadingDefs || len(paragraph) == 0 {
			leadingDefs = false
			paragraphLine = number
		}
		if paragraphBytes <= maxHeadingInlineBytes {
			paragraph = append(paragraph, text)
			paragraphBytes += len(text) + 1
		}
	}
	err := forEachLine(ctx, content, func(line []byte, number int) bool {
		l := cls.next(line)
		switch l.kind {
		case lineATX:
			paragraph = paragraph[:0]
			return add(l.level, atxTitle(l.rest, l.level), number, false)
		case lineSetext:
			if len(paragraph) == 0 {
				return true
			}
			raw := strings.Join(paragraph, "\n")
			paragraph = paragraph[:0]
			return add(l.level, raw, paragraphLine, true)
		case lineParagraphStart:
			paragraph, paragraphBytes, leadingDefs = paragraph[:0], 0, true
			paragraphLineText(l, number)
		case lineParagraphText:
			paragraphLineText(l, number)
		default:
			paragraph = paragraph[:0]
		}
		return true
	})
	if err != nil {
		return markdownHeadings{}, err
	}
	titles := newTitleRenderer(defs)
	for i := range out.headers {
		out.headers[i].Title = titles.render(raws[i].raw, raws[i].setext)
	}
	return out, nil
}

// Bounds of the link-reference definitions the fallback scanner keeps to render
// titles that use reference links.
const (
	maxFallbackLinkRefs     = 512
	maxFallbackLinkRefBytes = 64 << 10
)

// isLinkRefDefinition reports whether text looks like a one-line link-reference
// definition: a bracketed label followed by a colon and a destination.
func isLinkRefDefinition(text string) bool {
	if !strings.HasPrefix(text, "[") {
		return false
	}
	end := strings.Index(text, "]:")
	return end > 1 && strings.TrimSpace(text[end+2:]) != ""
}

// atxTitle returns the raw title of an ATX heading without its opening and
// optional closing sequence.
func atxTitle(rest []byte, level int) string {
	title := bytes.TrimSpace(rest[level:])
	if closing := bytes.TrimRight(title, "#"); len(closing) == 0 || closing[len(closing)-1] == ' ' || closing[len(closing)-1] == '\t' {
		title = bytes.TrimSpace(closing)
	}
	return string(title)
}

// titleRenderer renders raw heading titles as goldmark would. It reuses one
// parser, since titles are parsed one at a time, and appends the document's
// link-reference definitions to titles that may use them.
type titleRenderer struct {
	parser interface {
		Parse(reader text.Reader, opts ...parser.ParseOption) ast.Node
	}
	defs string
	// remaining and triggers are the title source and inline markup bytes this
	// document may still inline-parse; they mirror the heading-only parser's budgets.
	remaining, triggers int
}

// newTitleRenderer builds a renderer around the heading-only goldmark parser.
func newTitleRenderer(defs []string) *titleRenderer {
	r := &titleRenderer{parser: newHeadingOnlyParser(), remaining: maxHeadingInlineBytesTotal, triggers: maxHeadingInlineTriggersTotal}
	if len(defs) > 0 {
		r.defs = "\n\n" + strings.Join(defs, "\n") + "\n"
	}
	return r
}

// render returns raw as goldmark renders it in an ATX (or setext) heading. A title
// without inline markup is returned as is; a source over maxHeadingInlineBytes
// stays raw, as on the goldmark path.
func (r *titleRenderer) render(raw string, setext bool) string {
	raw = strings.TrimSpace(raw)
	triggers := countInlineTriggers([]byte(raw))
	if raw == "" || triggers == 0 || len(raw) > maxHeadingInlineBytes || len(raw) > r.remaining || triggers > r.triggers {
		return clampHeadingTitle(raw)
	}
	r.remaining -= len(raw)
	r.triggers -= triggers
	src := "# " + raw + "\n"
	if setext {
		src = raw + "\n===\n"
	}
	if strings.Contains(raw, "[") {
		src += r.defs
	}
	source := []byte(src)
	root := r.parser.Parse(text.NewReader(source))
	title := raw
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			title = strings.TrimSpace(headingText(h, source))
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return clampHeadingTitle(title)
}
