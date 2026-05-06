package pageindex

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// routedStubLLM dispatches Respond by inspecting the user input prompt body.
// We can't pre-hash because verify_test builds prompts at runtime.
type routedStubLLM struct {
	// responder is invoked per call with the rendered user prompt; returns a Response.
	responder func(prompt string) *Response
	calls     int32
	fixerCall int32
}

func (s *routedStubLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	atomic.AddInt32(&s.calls, 1)
	body := ""
	for _, it := range req.Input {
		body += it.Content
	}
	if strings.Contains(body, "find the physical index of the start page") {
		atomic.AddInt32(&s.fixerCall, 1)
	}
	resp := s.responder(body)
	if resp == nil {
		return &Response{Text: "{}"}, nil
	}
	cp := *resp
	return &cp, nil
}

func (s *routedStubLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, it := range req.Input {
		total += len(it.Content) / 4
	}
	return total, nil
}

func (s *routedStubLLM) Calls() int { return int(atomic.LoadInt32(&s.calls)) }
func (s *routedStubLLM) FixerCalls() int {
	return int(atomic.LoadInt32(&s.fixerCall))
}

// makeVerifyTestIndexer wires an Indexer with the routed stub.
func makeVerifyTestIndexer(t *testing.T, llm LLM) *Indexer {
	t.Helper()
	tk, err := NewTokenizer("gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultTestSettings()
	cfg.Algo.GenerateNodeSummary = false
	cfg.Algo.GenerateDocDescription = false
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tk, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// makeFlatTree returns a small tree with N siblings whose StartIndex maps
// directly onto a synthetic page list of the same length. titles[i] points at
// page i+1.
func makeFlatTree(titles []string) ([]*Node, []string) {
	roots := make([]*Node, len(titles))
	pages := make([]string, len(titles))
	for i, title := range titles {
		roots[i] = &Node{Title: title, StartIndex: i + 1, EndIndex: i + 1}
		pages[i] = title + " body content"
	}
	return roots, pages
}

func TestVerifyAndFixAllCorrect(t *testing.T) {
	stub := &routedStubLLM{
		responder: func(_ string) *Response {
			return &Response{Text: `{"answer":"yes"}`}
		},
	}
	idx := makeVerifyTestIndexer(t, stub)
	roots, pages := makeFlatTree([]string{"Intro", "Methods", "Results", "Conclusion"})
	tree := &Tree{Structure: roots}
	stats := &Stats{}
	if err := idx.verifyAndFix(context.Background(), tree, pages, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("verifyAndFix returned err: %v", err)
	}
	if stub.FixerCalls() != 0 {
		t.Errorf("expected zero fixer calls when accuracy=1.0, got %d", stub.FixerCalls())
	}
	if stub.Calls() != len(roots) {
		t.Errorf("expected %d check calls, got %d", len(roots), stub.Calls())
	}
	// Tree must remain unchanged.
	for i, n := range tree.Structure {
		if n.StartIndex != i+1 {
			t.Errorf("node %d StartIndex mutated: got %d", i, n.StartIndex)
		}
	}
}

func TestVerifyAndFixSomeIncorrectFixerSucceeds(t *testing.T) {
	// 4 nodes: indices 1..4. Node #2 (Methods) is "wrong"; fixer returns
	// page 2 — the post-fixer check at page=2 should validate. We track
	// fixer calls so we can switch the post-fix verifier to "yes".
	var fixerSeen int32
	stub := &routedStubLLM{
		responder: func(prompt string) *Response {
			if strings.Contains(prompt, "find the physical index of the start page") {
				atomic.AddInt32(&fixerSeen, 1)
				return &Response{Text: `{"physical_index":"<physical_index_2>"}`}
			}
			// Once the fixer has run at least once, every subsequent
			// title-appearance check is the post-fix re-validation — return
			// yes so the fix commits.
			if atomic.LoadInt32(&fixerSeen) > 0 {
				return &Response{Text: `{"answer":"yes"}`}
			}
			// During the verify sweep, only Methods is reported as missing.
			if strings.Contains(prompt, "Methods") {
				return &Response{Text: `{"answer":"no"}`}
			}
			return &Response{Text: `{"answer":"yes"}`}
		},
	}
	idx := makeVerifyTestIndexer(t, stub)
	roots, pages := makeFlatTree([]string{"Intro", "Methods", "Results", "Conclusion"})
	// Move "Methods" off its real page so the verify sweep registers it as
	// incorrect; the fixer is supposed to bring it back to page 2.
	roots[1].StartIndex = 99 // out-of-range so verify treats it as incorrect
	roots[1].EndIndex = 99
	tree := &Tree{Structure: roots}
	stats := &Stats{}
	if err := idx.verifyAndFix(context.Background(), tree, pages, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("verifyAndFix returned err: %v", err)
	}
	if stub.FixerCalls() == 0 {
		t.Errorf("expected fixer to be invoked when item incorrect; got 0 fixer calls")
	}
	if got := tree.Structure[1].StartIndex; got != 2 {
		t.Errorf("expected Methods StartIndex restored to 2, got %d", got)
	}
}

func TestVerifyAndFixFixerStaysWrongAfterRetries(t *testing.T) {
	// Fixer always returns a still-wrong page (1) and check_title_appearance
	// always says no for "Methods" no matter the page. The retry loop should
	// give up after titleCheckMaxRetries attempts and leave the node alone.
	stub := &routedStubLLM{
		responder: func(prompt string) *Response {
			if strings.Contains(prompt, "find the physical index of the start page") {
				return &Response{Text: `{"physical_index":1}`}
			}
			if strings.Contains(prompt, "Methods") {
				return &Response{Text: `{"answer":"no"}`}
			}
			return &Response{Text: `{"answer":"yes"}`}
		},
	}
	idx := makeVerifyTestIndexer(t, stub)
	roots, pages := makeFlatTree([]string{"Intro", "Methods", "Results", "Conclusion"})
	originalStart := roots[1].StartIndex
	tree := &Tree{Structure: roots}
	stats := &Stats{}
	if err := idx.verifyAndFix(context.Background(), tree, pages, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("verifyAndFix returned err: %v", err)
	}
	if stub.FixerCalls() == 0 {
		t.Errorf("expected fixer to be invoked at least once")
	}
	// Up to 3 retries — but we should not exceed that.
	if stub.FixerCalls() > titleCheckMaxRetries {
		t.Errorf("expected at most %d fixer calls, got %d", titleCheckMaxRetries, stub.FixerCalls())
	}
	// Per upstream when invalid persists, the entry's physical_index stays at
	// whatever the fixer last produced (1) is *not* committed because verify
	// said "no" — node should still be its original (incorrect-but-untouched)
	// or remain at the final fixer's invalid value. In this stub, since
	// post-fixer check returns "no", commit is skipped → StartIndex remains
	// the original.
	if got := tree.Structure[1].StartIndex; got != originalStart {
		t.Errorf("expected Methods StartIndex to remain %d when fix never validates, got %d",
			originalStart, got)
	}
}

func TestVerifyAndFixSingleNodeNoOp(t *testing.T) {
	stub := &routedStubLLM{
		responder: func(_ string) *Response {
			return &Response{Text: `{"answer":"yes"}`}
		},
	}
	idx := makeVerifyTestIndexer(t, stub)
	roots, pages := makeFlatTree([]string{"OnlyNode"})
	tree := &Tree{Structure: roots}
	stats := &Stats{}
	if err := idx.verifyAndFix(context.Background(), tree, pages, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("verifyAndFix returned err: %v", err)
	}
	if stub.Calls() != 0 {
		t.Errorf("expected verify to be a no-op for single-node trees, got %d calls", stub.Calls())
	}
}

func TestVerifyAndFixLowAccuracyLogsAndProceeds(t *testing.T) {
	// Force everything to "no": accuracy=0, which is ≤0.6 → no fix attempted.
	stub := &routedStubLLM{
		responder: func(prompt string) *Response {
			if strings.Contains(prompt, "find the physical index of the start page") {
				t.Errorf("fixer must not run when accuracy ≤ 0.6")
				return &Response{Text: `{"physical_index":1}`}
			}
			return &Response{Text: `{"answer":"no"}`}
		},
	}
	idx := makeVerifyTestIndexer(t, stub)
	roots, pages := makeFlatTree([]string{"A", "B", "C", "D"})
	tree := &Tree{Structure: roots}
	stats := &Stats{}
	if err := idx.verifyAndFix(context.Background(), tree, pages, NewBudget(1<<20), stats); err != nil {
		t.Fatalf("verifyAndFix returned err: %v", err)
	}
	if stub.FixerCalls() != 0 {
		t.Errorf("expected no fixer calls when accuracy ≤ 0.6, got %d", stub.FixerCalls())
	}
}
