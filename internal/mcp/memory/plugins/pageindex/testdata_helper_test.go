package pageindex

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
)

// minimalPDFJSON describes a 3-page PDF that pdfcpu can render via api.Create.
const minimalPDFJSON = `{
  "pages": {
    "1": {"content": {"text": [{"value": "Hello world page one. This document is a sample for the pageindex plugin tests.", "font": {"name": "Helvetica","size": 12}, "pos": [50, 700]}]}},
    "2": {"content": {"text": [{"value": "Page two body covers methods of the test fixture.", "font": {"name": "Helvetica","size": 12}, "pos": [50, 700]}]}},
    "3": {"content": {"text": [{"value": "Page three body has the conclusion paragraph.", "font": {"name": "Helvetica","size": 12}, "pos": [50, 700]}]}}
  }
}`

var (
	samplePDFOnce  sync.Once
	samplePDFBytes []byte
	samplePDFErr   error
)

// loadSamplePDF renders a fresh in-memory fixture once per test process.
// Never reuse an on-disk PDF: stale fixtures can hide generator regressions.
func loadSamplePDF(t *testing.T) []byte {
	t.Helper()
	samplePDFOnce.Do(func() {
		var buf bytes.Buffer
		if err := pdfapi.Create(context.Background(), nil, strings.NewReader(minimalPDFJSON), &buf, nil); err != nil {
			samplePDFErr = err
			return
		}
		samplePDFBytes = buf.Bytes()
	})
	if samplePDFErr != nil {
		t.Fatalf("could not generate sample.pdf: %v", samplePDFErr)
	}
	return samplePDFBytes
}
