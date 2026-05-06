package pageindex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
)

// minimalPDFJSON describes a 3-page PDF that pdfcpu can render via api.Create.
const minimalPDFJSON = `{
  "header": { "title": "pageindex sample", "author": "test" },
  "pages": {
    "1": {"content": {"text": [{"value": "Hello world page one. This document is a sample for the pageindex plugin tests.", "font": {"name": "Helvetica","size": 12}, "position": [50, 700]}]}},
    "2": {"content": {"text": [{"value": "Page two body covers methods of the test fixture.", "font": {"name": "Helvetica","size": 12}, "position": [50, 700]}]}},
    "3": {"content": {"text": [{"value": "Page three body has the conclusion paragraph.", "font": {"name": "Helvetica","size": 12}, "position": [50, 700]}]}}
  }
}`

var (
	samplePDFOnce  sync.Once
	samplePDFBytes []byte
	samplePDFErr   error
)

// loadSamplePDF lazily renders or reads the test PDF in testdata/.
func loadSamplePDF(t *testing.T) []byte {
	t.Helper()
	samplePDFOnce.Do(func() {
		path := filepath.Join("testdata", "sample.pdf")
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			samplePDFBytes = data
			return
		}
		var buf bytes.Buffer
		if err := pdfapi.Create(nil, strings.NewReader(minimalPDFJSON), &buf, nil); err != nil {
			samplePDFErr = err
			return
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			samplePDFErr = err
			return
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			samplePDFErr = err
			return
		}
		samplePDFBytes = buf.Bytes()
	})
	if samplePDFErr != nil {
		t.Skipf("could not generate sample.pdf: %v", samplePDFErr)
	}
	return samplePDFBytes
}
