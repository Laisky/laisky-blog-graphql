package pageindex

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// expandStubLLM returns a canned generate_toc_init JSON that produces
// children when invoked. Tracks calls so depth-ceiling tests can assert
// zero LLM activity.
type expandStubLLM struct {
	calls    int32
	respText string
}

func (s *expandStubLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	atomic.AddInt32(&s.calls, 1)
	body := ""
	for _, it := range req.Input {
		body += it.Content
	}
	// Only respond to generate-toc-init / generate-toc-continue prompts.
	if strings.Contains(body, "generate the tree structure") ||
		strings.Contains(body, "continue the tree structure") {
		return &Response{Text: s.respText}, nil
	}
	return &Response{Text: "{}"}, nil
}
func (s *expandStubLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, it := range req.Input {
		total += len(it.Content) / 4
	}
	return total, nil
}
func (s *expandStubLLM) Calls() int { return int(atomic.LoadInt32(&s.calls)) }

// fatTokenizer reports a per-string count high enough to push expansion past
// the configured token threshold no matter the input length.
type fatTokenizer struct{ perCall int }

func (f *fatTokenizer) Count(_ string) int { return f.perCall }
func (f *fatTokenizer) Encode(_ string) []int {
	return nil
}

func TestExpandLargeNodeProducesChildren(t *testing.T) {
	// 20 pages, max_page_num_each_node=2, max_token_num_each_node=10. The
	// fatTokenizer reports 100 tokens per page so the threshold trips.
	pages := make([]string, 20)
	for i := range pages {
		pages[i] = "page body " + string(rune('A'+i))
	}
	respJSON := `[
        {"structure":"1","title":"Section A","physical_index":"<physical_index_1>"},
        {"structure":"2","title":"Section B","physical_index":"<physical_index_10>"},
        {"structure":"3","title":"Section C","physical_index":"<physical_index_15>"}
    ]`
	stub := &expandStubLLM{respText: respJSON}
	cfg := defaultTestSettings()
	cfg.Algo.MaxPageNumEachNode = 2
	cfg.Algo.MaxTokenNumEachNode = 50
	idx, err := NewIndexer(Deps{LLM: stub, Tokenizer: &fatTokenizer{perCall: 100}, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	root := &Node{Title: "Document", StartIndex: 1, EndIndex: 20}
	stats := &Stats{}
	if err := idx.expandLargeNodes(context.Background(), []*Node{root}, pages, 0, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("expandLargeNodes: %v", err)
	}
	if len(root.Children) == 0 {
		t.Fatalf("expected children after expansion, got none")
	}
	if stub.Calls() == 0 {
		t.Errorf("expected at least one LLM call to drive expansion")
	}
	for _, c := range root.Children {
		if c.StartIndex <= 0 {
			t.Errorf("child %q missing StartIndex", c.Title)
		}
		if c.StartIndex < root.StartIndex || c.StartIndex > 20 {
			t.Errorf("child %q StartIndex %d out of parent range [%d,%d]",
				c.Title, c.StartIndex, root.StartIndex, root.EndIndex)
		}
	}
}

func TestExpandDepthCeiling(t *testing.T) {
	// At depth=3 we must short-circuit without any LLM calls regardless of
	// the node's size.
	pages := make([]string, 50)
	for i := range pages {
		pages[i] = "page body"
	}
	stub := &expandStubLLM{respText: `[]`}
	cfg := defaultTestSettings()
	cfg.Algo.MaxPageNumEachNode = 1
	cfg.Algo.MaxTokenNumEachNode = 1
	idx, err := NewIndexer(Deps{LLM: stub, Tokenizer: &fatTokenizer{perCall: 1000}, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	root := &Node{Title: "Huge", StartIndex: 1, EndIndex: 50}
	stats := &Stats{}
	if err := idx.expandLargeNodes(context.Background(), []*Node{root}, pages, maxExpansionDepth, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("expandLargeNodes: %v", err)
	}
	if stub.Calls() != 0 {
		t.Errorf("expected zero LLM calls at depth=%d, got %d",
			maxExpansionDepth, stub.Calls())
	}
	if len(root.Children) != 0 {
		t.Errorf("expected node to remain a leaf at depth ceiling, got %d children", len(root.Children))
	}
}

func TestExpandSkipsSmallNode(t *testing.T) {
	pages := make([]string, 5)
	for i := range pages {
		pages[i] = "small"
	}
	stub := &expandStubLLM{respText: `[]`}
	cfg := defaultTestSettings()
	cfg.Algo.MaxPageNumEachNode = 10
	cfg.Algo.MaxTokenNumEachNode = 100000
	idx, err := NewIndexer(Deps{LLM: stub, Tokenizer: &fatTokenizer{perCall: 1}, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	root := &Node{Title: "Small", StartIndex: 1, EndIndex: 5}
	stats := &Stats{}
	if err := idx.expandLargeNodes(context.Background(), []*Node{root}, pages, 0, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("expandLargeNodes: %v", err)
	}
	if stub.Calls() != 0 {
		t.Errorf("expected zero LLM calls for small nodes, got %d", stub.Calls())
	}
	if len(root.Children) != 0 {
		t.Errorf("small node must remain a leaf, got %d children", len(root.Children))
	}
}
