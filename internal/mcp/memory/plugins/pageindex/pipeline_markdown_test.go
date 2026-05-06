package pageindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkdownPipelineNoSummary(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample.md"))
	if err != nil {
		t.Fatal(err)
	}
	tk, err := NewTokenizer("gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	stub := NewStubLLM()
	cfg := defaultTestSettings()
	cfg.Algo.GenerateNodeSummary = false
	idx, err := NewIndexer(Deps{LLM: stub, Tokenizer: tk, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	tree, stats, err := idx.Index(context.Background(), KindMarkdown, body, IndexOptions{DocID: "doc1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Structure) == 0 {
		t.Fatal("expected non-empty tree structure")
	}
	if stats.LLMCalls != 0 {
		t.Fatalf("expected 0 llm calls, got %d", stats.LLMCalls)
	}
}

func defaultTestSettings() Settings {
	return Settings{
		Indexer:   IndexerSettings{MaxConcurrency: 4},
		Algo:      AlgoSettings{TocCheckPageNum: 5, MaxPageNumEachNode: 2, MaxTokenNumEachNode: 8000, GenerateNodeSummary: false, GenerateDocDescription: false},
		TreeQuery: TreeQuerySettings{MaxSteps: 3, MaxTokens: 5000, CandidateDocs: 3},
		PDF:       PDFSettings{TextParser: "pdfcpu", OutlineParser: "pdfcpu"},
	}
}
