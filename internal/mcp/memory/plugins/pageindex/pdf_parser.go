package pageindex

import (
	"bytes"
	"context"

	errors "github.com/Laisky/errors/v2"
	dpdf "github.com/dslipak/pdf"
	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpu "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

// Bookmark mirrors a recursive PDF outline entry.
type Bookmark struct {
	Title    string
	PageFrom int
	Children []Bookmark
}

// PDFParser abstracts pure-Go PDF text + outline extraction.
type PDFParser interface {
	PageCount(ctx context.Context, data []byte) (int, error)
	PageText(ctx context.Context, data []byte, page int) (string, error)
	PagesText(ctx context.Context, data []byte) ([]string, error)
	Outline(ctx context.Context, data []byte) ([]Bookmark, error)
}

// NewPDFParser dispatches text and outline parsers by name. Both default to "pdfcpu".
func NewPDFParser(text, outline string) (PDFParser, error) {
	if text == "" {
		text = parserPdfcpu
	}
	if outline == "" {
		outline = parserPdfcpu
	}
	switch text {
	case parserPdfcpu, parserDslipak:
	default:
		return nil, errors.Errorf("unknown text parser %q", text)
	}
	if outline != parserPdfcpu && outline != parserDslipak {
		return nil, errors.Errorf("unknown outline parser %q", outline)
	}
	return &pdfBackend{text: text, outline: outline}, nil
}

type pdfBackend struct {
	text    string
	outline string
}

// PageCount returns the document's total page count.
func (p *pdfBackend) PageCount(ctx context.Context, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, errors.Wrap(err, "pdf page count canceled")
	}
	rs := bytes.NewReader(data)
	switch p.text {
	case parserPdfcpu:
		// Forward the request lifetime to pdfcpu, including cancellation.
		n, err := pdfapi.PageCount(ctx, rs, nil)
		if err != nil {
			return 0, errors.Wrap(err, "pdfcpu page count")
		}
		return n, nil
	default:
		// dslipak demands a *Reader, requires ReaderAt + size.
		r, err := dpdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return 0, errors.Wrap(err, "dslipak open")
		}
		return r.NumPage(), nil
	}
}

// PageText extracts plain text for a single 1-indexed page.
func (p *pdfBackend) PageText(ctx context.Context, data []byte, page int) (string, error) {
	pages, err := dslipakPages(ctx, data)
	if err != nil {
		return "", err
	}
	if page < 1 || page > len(pages) {
		return "", errors.Errorf("page %d out of range [1,%d]", page, len(pages))
	}
	return pages[page-1], nil
}

// PagesText extracts plain text for every page. We rely on dslipak for text in
// both modes since pdfcpu's API exposes raw content streams rather than text.
func (p *pdfBackend) PagesText(ctx context.Context, data []byte) ([]string, error) {
	return dslipakPages(ctx, data)
}

// Outline returns the recursive bookmark tree.
func (p *pdfBackend) Outline(ctx context.Context, data []byte) ([]Bookmark, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, "pdf outline canceled")
	}
	if p.outline == parserDslipak {
		return nil, errors.New("outline not supported by dslipak parser")
	}
	bms, err := pdfapi.Bookmarks(ctx, bytes.NewReader(data), nil)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, errors.Wrap(ctxErr, "pdf outline canceled")
		}
		// Preserve the existing no-outline fallback, but never swallow cancellation.
		return nil, nil
	}
	return convertBookmarks(bms), nil
}

func dslipakPages(ctx context.Context, data []byte) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, "pdf text canceled")
	}
	r, err := dpdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.Wrap(err, "dslipak open")
	}
	n := r.NumPage()
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, errors.Wrap(err, "pdf text canceled")
		}
		page := r.Page(i)
		fonts := map[string]*dpdf.Font{}
		for _, name := range page.Fonts() {
			f := page.Font(name)
			fonts[name] = &f
		}
		text, err := page.GetPlainText(fonts)
		if err != nil {
			out = append(out, "")
			continue
		}
		out = append(out, text)
	}
	return out, nil
}

func convertBookmarks(in []pdfcpu.Bookmark) []Bookmark {
	if len(in) == 0 {
		return nil
	}
	out := make([]Bookmark, 0, len(in))
	for _, b := range in {
		out = append(out, Bookmark{Title: b.Title, PageFrom: b.PageFrom, Children: convertBookmarks(b.Kids)})
	}
	return out
}
