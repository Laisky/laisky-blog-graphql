package pageindex

import (
	"context"
	"testing"
)

func TestPDFPagesText(t *testing.T) {
	data := loadSamplePDF(t)
	parser, err := NewPDFParser("pdfcpu", "pdfcpu")
	if err != nil {
		t.Fatal(err)
	}
	n, err := parser.PageCount(context.Background(), data)
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 page, got %d", n)
	}
	pages, err := parser.PagesText(context.Background(), data)
	if err != nil {
		t.Fatalf("pages text: %v", err)
	}
	if len(pages) != n {
		t.Fatalf("expected %d page texts, got %d", n, len(pages))
	}
}

func TestDslipakOutlineRejected(t *testing.T) {
	data := loadSamplePDF(t)
	parser, err := NewPDFParser("dslipak", "dslipak")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Outline(context.Background(), data); err == nil {
		t.Fatal("expected dslipak outline to fail")
	}
}
