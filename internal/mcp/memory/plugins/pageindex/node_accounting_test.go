package pageindex

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// firstPageTag finds the first physical_index tag of a generation prompt.
var firstPageTag = regexp.MustCompile(`<physical_index_(\d+)>`)

// sliceTOCLLM answers each table-of-contents generation prompt with the canned
// list registered for the first page of the slice it covers, and with an empty
// list otherwise, so a test controls exactly which node is expanded into what.
type sliceTOCLLM struct {
	mu      sync.Mutex
	byFirst map[int]string
	calls   int
}

// Respond answers generation prompts by the slice's first page.
func (s *sliceTOCLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := ""
	for _, item := range req.Input {
		prompt += item.Content
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if !strings.Contains(prompt, "generate the tree structure") && !strings.Contains(prompt, "continue the tree structure") {
		return &Response{Text: "{}"}, nil
	}
	m := firstPageTag.FindStringSubmatch(prompt)
	if m == nil {
		return &Response{Text: "[]"}, nil
	}
	page, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, err
	}
	if body, ok := s.byFirst[page]; ok {
		return &Response{Text: body}, nil
	}
	return &Response{Text: "[]"}, nil
}

// CountTokens approximates the prompt size.
func (s *sliceTOCLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, item := range req.Input {
		total += len(item.Content) / 4
	}
	return total, nil
}

// newExpansionIndexer builds an indexer whose every node over two pages is large
// enough to expand.
func newExpansionIndexer(t *testing.T, llm LLM) *Indexer {
	t.Helper()
	cfg := defaultTestSettings()
	cfg.Algo.MaxPageNumEachNode = 2
	cfg.Algo.MaxTokenNumEachNode = 50
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: &fatTokenizer{perCall: 100}, Settings: cfg})
	require.NoError(t, err)
	return idx
}

// childTitles returns the titles of nodes in order.
func childTitles(nodes []*Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Title)
	}
	return out
}

// TestExpansionReservesOnlyTheNodesItInserts reproduces the P2 finding of the
// third review of #96: expandNode reserved every generated node before it
// dropped the first one, which repeats the parent's title. With room for exactly
// the two children that are inserted, the expansion was refused and the tree
// marked truncated.
func TestExpansionReservesOnlyTheNodesItInserts(t *testing.T) {
	llm := &sliceTOCLLM{byFirst: map[int]string{1: `[
        {"structure":"1","title":"Document","physical_index":"<physical_index_1>"},
        {"structure":"2","title":"Section A","physical_index":"<physical_index_5>"},
        {"structure":"3","title":"Section B","physical_index":"<physical_index_12>"}
    ]`}}
	idx := newExpansionIndexer(t, llm)
	pages := make([]string, 20)
	for i := range pages {
		pages[i] = "page " + strconv.Itoa(i+1)
	}
	root := &Node{Title: "Document", StartIndex: 1, EndIndex: 20}
	budget := newOperationBudget(1<<30, 100, 2, "")
	stats := &Stats{}

	require.NoError(t, idx.expandLargeNodes(context.Background(), []*Node{root}, pages, 0, budget, stats))
	require.Equal(t, []string{"Section A", "Section B"}, childTitles(root.Children), "the two inserted children fit the two free nodes")
	require.False(t, stats.StructureTruncated, "nothing was cut short")
	require.Equal(t, 0, budget.nodesLeft(), "exactly the inserted nodes are claimed")
	require.Equal(t, 4, root.EndIndex, "the parent keeps only the pages before its first child")
}

// TestExpansionPromotesChildrenOfTheParentTitledEntry covers the same branch: the
// generated entry that repeats the parent's title was dropped together with its
// children, although the upstream algorithm (process_large_node_recursively)
// drops only that entry and keeps the sections nested under it. They are
// promoted, and only the promoted forest is reserved.
func TestExpansionPromotesChildrenOfTheParentTitledEntry(t *testing.T) {
	llm := &sliceTOCLLM{byFirst: map[int]string{1: `[
        {"structure":"1","title":"Document","physical_index":"<physical_index_1>"},
        {"structure":"1.1","title":"Intro","physical_index":"<physical_index_3>"},
        {"structure":"1.2","title":"Background","physical_index":"<physical_index_6>"},
        {"structure":"2","title":"Results","physical_index":"<physical_index_12>"}
    ]`}}
	idx := newExpansionIndexer(t, llm)
	pages := make([]string, 20)
	for i := range pages {
		pages[i] = "page " + strconv.Itoa(i+1)
	}
	root := &Node{Title: " Document ", StartIndex: 1, EndIndex: 20}
	budget := newOperationBudget(1<<30, 100, 3, "")
	stats := &Stats{}

	require.NoError(t, idx.expandLargeNodes(context.Background(), []*Node{root}, pages, 0, budget, stats))
	require.Equal(t, []string{"Intro", "Background", "Results"}, childTitles(root.Children), "the sections under the repeated title are kept")
	require.False(t, stats.StructureTruncated)
	require.Equal(t, 0, budget.nodesLeft())
	require.Equal(t, 2, root.EndIndex)
	require.Equal(t, 5, root.Children[0].EndIndex)
	require.Equal(t, 11, root.Children[1].EndIndex)
	require.Equal(t, 20, root.Children[2].EndIndex)
}

// TestExpansionThatDoesNotFitStaysALeaf checks the refusal side of the boundary:
// one node short, the expansion is skipped whole, nothing is claimed and the run
// is marked truncated.
func TestExpansionThatDoesNotFitStaysALeaf(t *testing.T) {
	llm := &sliceTOCLLM{byFirst: map[int]string{1: `[
        {"structure":"1","title":"Document","physical_index":"<physical_index_1>"},
        {"structure":"2","title":"Section A","physical_index":"<physical_index_5>"},
        {"structure":"3","title":"Section B","physical_index":"<physical_index_12>"}
    ]`}}
	idx := newExpansionIndexer(t, llm)
	pages := make([]string, 20)
	root := &Node{Title: "Document", StartIndex: 1, EndIndex: 20}
	budget := newOperationBudget(1<<30, 100, 1, "")
	stats := &Stats{}

	require.NoError(t, idx.expandLargeNodes(context.Background(), []*Node{root}, pages, 0, budget, stats))
	require.Empty(t, root.Children)
	require.Equal(t, 20, root.EndIndex, "a skipped expansion keeps the whole page range")
	require.True(t, stats.StructureTruncated)
	require.Equal(t, 1, budget.nodesLeft(), "a refused expansion claims nothing")
}

// pdfTOCLLM answers the PDF pipeline's prompts: no printed table of contents, a
// canned generated one, and affirmative verification.
type pdfTOCLLM struct{ toc string }

// Respond answers by prompt kind.
func (p pdfTOCLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := ""
	for _, item := range req.Input {
		prompt += item.Content
	}
	switch {
	case strings.Contains(prompt, "detect if there is a table of content"):
		return &Response{Text: `{"thinking":"none","toc_detected":"no"}`}, nil
	case strings.Contains(prompt, "generate the tree structure of the document"):
		return &Response{Text: p.toc}, nil
	case strings.Contains(prompt, "check if the given section appears or starts"):
		return &Response{Text: `{"thinking":"ok","answer":"yes"}`}, nil
	}
	return &Response{Text: "[]"}, nil
}

// CountTokens approximates the prompt size.
func (pdfTOCLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, item := range req.Input {
		total += len(item.Content) / 4
	}
	return total, nil
}

// TestPDFNodeCeilingCountsOnlyAssembledEntries sweeps the PDF assembly for the
// same defect: table-of-contents entries without a title were counted against
// max_nodes_per_document, and cut the list, although the assembly drops them, so
// valid sections were lost and the tree was marked truncated.
func TestPDFNodeCeilingCountsOnlyAssembledEntries(t *testing.T) {
	cfg := defaultTestSettings()
	cfg.Limits = LimitsSettings{MaxNodesPerDocument: 3}
	llm := pdfTOCLLM{toc: `[
        {"structure":"1","title":"","physical_index":"<physical_index_1>"},
        {"structure":"2","title":"Alpha","physical_index":"<physical_index_1>"},
        {"structure":"3","title":"  ","physical_index":"<physical_index_2>"},
        {"structure":"4","title":"Beta","physical_index":"<physical_index_2>"},
        {"structure":"5","title":"Gamma","physical_index":"<physical_index_3>"}
    ]`}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"one", "two", "three"}}, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)

	tree, stats, err := idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "pdf"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"Alpha", "Beta", "Gamma"}, childTitles(tree.Structure), "every titled entry fits the ceiling")
	require.False(t, stats.StructureTruncated, "untitled entries are not structure")
}

// TestPDFUntitledTableOfContentsFallsBackToOneNode checks that a generated table
// of contents with no usable title gets the documented single Document node
// instead of an empty structure.
func TestPDFUntitledTableOfContentsFallsBackToOneNode(t *testing.T) {
	cfg := defaultTestSettings()
	llm := pdfTOCLLM{toc: `[{"structure":"1","title":" ","physical_index":"<physical_index_1>"}]`}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"one", "two"}}, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)

	tree, _, err := idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "pdf"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"Document"}, childTitles(tree.Structure))
	require.Equal(t, 1, tree.Structure[0].StartIndex)
	require.Equal(t, 2, tree.Structure[0].EndIndex)
}
