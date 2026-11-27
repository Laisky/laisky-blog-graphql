package pageindex

import (
	"context"
	"testing"

	errors "github.com/Laisky/errors/v2"
)

func TestSearchLoopMergeAndLimit(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	for i, id := range []string{"d1", "d2"} {
		tree := &Tree{
			DocID:     id,
			Type:      KindPDF,
			PageCount: 4,
			Structure: []*Node{{Title: "Root", StartIndex: 1, EndIndex: 4}},
			Pages: []Page{
				{Page: 1, Content: "page-1-of-" + id},
				{Page: 2, Content: "page-2-of-" + id},
			},
		}
		if err := store.PutTree(ctx, "p", id, tree); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateIndexEntry(ctx, "p", "/d"+string(rune('a'+i))+".pdf", IndexEntry{DocID: id, Type: "pdf"}); err != nil {
			t.Fatal(err)
		}
	}
	stub := NewStubLLM()
	stub.SetDefault(TextResponse(`{"ranges":[{"start":1,"end":2,"reason":"top"}]}`))
	idx := &Indexer{} // GetPageContent only needs the receiver to be non-nil.
	cfg := defaultTestSettings()
	searcher := NewSearcher(stub, store, idx, cfg)
	res, err := searcher.Run(ctx, SearchInput{Project: "p", Query: "what?", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) > 3 {
		t.Fatalf("limit not enforced: %d", len(res.Chunks))
	}
	if len(res.Chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	prev := res.Chunks[0].Score
	for _, c := range res.Chunks[1:] {
		if c.Score > prev {
			t.Fatalf("chunks not score-sorted: %v", res.Chunks)
		}
		prev = c.Score
	}
}

// TestSearchLoopBudgetExhaustion (P04) — with MaxSteps=1 and three candidates,
// the search loop visits at most one document and returns its chunks without
// erroring. The remaining candidates contribute no chunks. This exercises the
// step-budget short-circuit in Searcher.Run.
func TestSearchLoopBudgetExhaustion(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	for i, id := range []string{"d1", "d2", "d3"} {
		tree := &Tree{
			DocID:     id,
			Type:      KindPDF,
			PageCount: 2,
			Structure: []*Node{{Title: "Root", StartIndex: 1, EndIndex: 2}},
			Pages: []Page{
				{Page: 1, Content: "p1-" + id},
				{Page: 2, Content: "p2-" + id},
			},
		}
		if err := store.PutTree(ctx, "p", id, tree); err != nil {
			t.Fatal(err)
		}
		path := "/d" + string(rune('a'+i)) + ".pdf"
		if err := store.UpdateIndexEntry(ctx, "p", path, IndexEntry{DocID: id, Type: "pdf"}); err != nil {
			t.Fatal(err)
		}
	}
	stub := NewStubLLM()
	stub.SetDefault(TextResponse(`{"ranges":[{"start":1,"end":2,"reason":"all"}]}`))
	idx := &Indexer{}
	cfg := defaultTestSettings()
	cfg.TreeQuery.MaxSteps = 1
	cfg.TreeQuery.CandidateDocs = 3
	searcher := NewSearcher(stub, store, idx, cfg)
	res, err := searcher.Run(ctx, SearchInput{Project: "p", Query: "what?", Limit: 100})
	if err != nil {
		t.Fatalf("search returned error after step exhaustion, want partial result: %v", err)
	}
	// Only the first candidate should have produced chunks.
	if got := len(res.Chunks); got == 0 || got > 2 {
		t.Fatalf("step-budget partial result must contain chunks from one doc only, got %d chunks", got)
	}
	// All remaining chunks must come from the first candidate path.
	first := res.Chunks[0].FilePath
	for _, c := range res.Chunks[1:] {
		if c.FilePath != first {
			t.Fatalf("step-budget exhaustion leaked into doc %q (first=%q)", c.FilePath, first)
		}
	}
}

// TestSearchLoopMalformedJSONFallback (P05) — when LLM.Respond returns a body
// that is not the expected JSON shape (or errors entirely), the search loop
// must degrade to the "first node of each candidate" fallback rather than
// surfacing the error to the caller.
func TestSearchLoopMalformedJSONFallback(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{
		DocID:     "d1",
		Type:      KindPDF,
		PageCount: 2,
		Structure: []*Node{{Title: "Root", StartIndex: 1, EndIndex: 1}},
		Pages: []Page{
			{Page: 1, Content: "first-page"},
			{Page: 2, Content: "second-page"},
		},
	}
	if err := store.PutTree(ctx, "p", "d1", tree); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIndexEntry(ctx, "p", "/d1.pdf", IndexEntry{DocID: "d1", Type: "pdf"}); err != nil {
		t.Fatal(err)
	}
	// errLLM mimics a Responses-API strict-schema rejection: the call surfaces
	// an error to Searcher.pickRanges, which must catch it and use the
	// firstNodeRange fallback.
	llm := errLLM{err: errors.New("schema validation failed")}
	idx := &Indexer{}
	cfg := defaultTestSettings()
	searcher := NewSearcher(llm, store, idx, cfg)
	res, err := searcher.Run(ctx, SearchInput{Project: "p", Query: "anything", Limit: 5})
	if err != nil {
		t.Fatalf("malformed LLM response should fall back, not error: %v", err)
	}
	if len(res.Chunks) == 0 {
		t.Fatalf("fallback must yield at least the first node's pages")
	}
	if res.Chunks[0].ChunkContent != "first-page" {
		t.Fatalf("fallback should surface the first node's page content, got %q", res.Chunks[0].ChunkContent)
	}
}

type errLLM struct{ err error }

func (e errLLM) Respond(_ context.Context, _ Request) (*Response, error) { return nil, e.err }
func (e errLLM) CountTokens(_ context.Context, _ Request) (int, error)   { return 0, nil }
