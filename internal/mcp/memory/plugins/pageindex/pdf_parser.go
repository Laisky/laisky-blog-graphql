package pageindex

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

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
		text = "pdfcpu"
	}
	if outline == "" {
		outline = "pdfcpu"
	}
	switch text {
	case "pdfcpu", "dslipak":
	default:
		return nil, errors.Errorf("unknown text parser %q", text)
	}
	if outline != "pdfcpu" && outline != "dslipak" {
		return nil, errors.Errorf("unknown outline parser %q", outline)
	}
	return &pdfBackend{text: text, outline: outline}, nil
}

type pdfBackend struct {
	text    string
	outline string
}

// PageCount returns the document's total page count.
func (p *pdfBackend) PageCount(_ context.Context, data []byte) (int, error) {
	rs := bytes.NewReader(data)
	switch p.text {
	case "pdfcpu":
		// pdfcpu's stable counter is PageCount(rs, conf).
		n, err := pdfapi.PageCount(rs, nil)
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
func (p *pdfBackend) PageText(_ context.Context, data []byte, page int) (string, error) {
	pages, err := dslipakPages(data)
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
func (p *pdfBackend) PagesText(_ context.Context, data []byte) ([]string, error) {
	return dslipakPages(data)
}

// Outline returns the recursive bookmark tree.
func (p *pdfBackend) Outline(_ context.Context, data []byte) ([]Bookmark, error) {
	if p.outline == "dslipak" {
		return nil, errors.New("outline not supported by dslipak parser")
	}
	tmpFile, cleanup, err := writeTemp(data)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	f, err := os.Open(tmpFile)
	if err != nil {
		return nil, errors.Wrap(err, "open temp pdf")
	}
	defer f.Close()
	bms, err := pdfapi.Bookmarks(f, nil)
	if err != nil {
		// pdfcpu returns errors when the PDF lacks an outline. Treat that as no outline.
		return nil, nil
	}
	return convertBookmarks(bms), nil
}

func dslipakPages(data []byte) ([]string, error) {
	r, err := dpdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.Wrap(err, "dslipak open")
	}
	n := r.NumPage()
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
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

// writeTemp persists data to a temp file because pdfapi.Bookmarks needs a real file handle.
func writeTemp(data []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "pageindex-*")
	if err != nil {
		return "", func() {}, errors.Wrap(err, "mkdir temp")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "doc.pdf")
	f, err := os.Create(path)
	if err != nil {
		cleanup()
		return "", func() {}, errors.Wrap(err, "create temp pdf")
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, errors.Wrap(err, "write temp pdf")
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, errors.Wrap(err, "close temp pdf")
	}
	return path, cleanup, nil
}
