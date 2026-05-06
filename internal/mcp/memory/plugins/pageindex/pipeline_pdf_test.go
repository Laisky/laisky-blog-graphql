package pageindex

import (
	"context"
	"testing"
)

func TestPDFPipelineWithStubLLM(t *testing.T) {
	data := loadSamplePDF(t)
	tk, err := NewTokenizer("gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	stub := NewStubLLM()
	// Default: no TOC, generate-toc-init returns one item.
	stub.SetDefault(JSONResponse([]map[string]any{
		{"structure": "1", "title": "Document", "physical_index": "<physical_index_1>"},
	}))
	cfg := defaultTestSettings()
	cfg.Algo.GenerateNodeSummary = false
	cfg.Algo.GenerateDocDescription = false
	idx, err := NewIndexer(Deps{LLM: stub, Tokenizer: tk, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	tree, stats, err := idx.Index(context.Background(), KindPDF, data, IndexOptions{DocID: "pdf1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.PageCount < 1 {
		t.Fatalf("expected page count >= 1, got %d", tree.PageCount)
	}
	if len(tree.Structure) == 0 {
		t.Fatal("expected non-empty structure")
	}
	if stats.LLMCalls < 1 {
		t.Fatal("expected at least one LLM call")
	}
}
