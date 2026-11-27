package pageindex

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// putSearchDoc stores tree as the catalog entry userPath of project.
func putSearchDoc(t *testing.T, store *SysStore, project, userPath string, tree *Tree) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.PutTree(ctx, project, tree.DocID, tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, project, userPath, IndexEntry{DocID: tree.DocID, Type: string(tree.Type)}))
}

// manualTree is a PDF-shaped tree whose first section is unrelated to billing.
func manualTree(docID string) *Tree {
	return &Tree{
		DocID:     docID,
		Type:      KindPDF,
		PageCount: 5,
		Structure: []*Node{
			{Title: "Introduction", StartIndex: 1, EndIndex: 1, Summary: "What the product is."},
			{Title: "Installation", StartIndex: 2, EndIndex: 3, Summary: "Download and set up the agent."},
			{Title: "Payments", StartIndex: 4, EndIndex: 5, Summary: "How invoices are billed each month.", Children: []*Node{
				{Title: "Refunds", StartIndex: 5, EndIndex: 5, Summary: "Returning a charge."},
			}},
		},
		Pages: []Page{
			{Page: 1, Content: "intro page"},
			{Page: 2, Content: "install page"},
			{Page: 3, Content: "configure page"},
			{Page: 4, Content: "billing page"},
			{Page: 5, Content: "refund page"},
		},
	}
}

// chunkContents returns the content of every chunk in order.
func chunkContents(res []string) string { return strings.Join(res, ",") }

// TestDegradedSearchFallsBackToTheMatchingSection reproduces the P2 finding of
// the third review of #96: when the planner could not run, search returned the
// first section of every candidate, which is irrelevant whenever the answer is in
// a later section. The deterministic fallback picks the sections whose title or
// summary shares the query's terms.
func TestDegradedSearchFallsBackToTheMatchingSection(t *testing.T) {
	for name, tc := range map[string]struct {
		llm    LLM
		tokens int
		reason string
	}{
		"planner failed":          {llm: errLLM{err: errors.New("upstream unavailable")}, tokens: 1 << 20, reason: searchFallbackPlannerFailed},
		"search budget too small": {llm: &countingLLM{}, tokens: 8, reason: searchFallbackSearchLimit},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewSysStore(newMemoryFS())
			putSearchDoc(t, store, "p", "/manual.pdf", manualTree("manual"))
			cfg := defaultTestSettings()
			cfg.TreeQuery.MaxTokens = tc.tokens
			searcher := NewSearcher(tc.llm, store, &Indexer{}, cfg)

			res, err := searcher.Run(context.Background(), SearchInput{Project: "p", Query: "How are invoices billed?", Limit: 5})
			require.NoError(t, err)
			contents := make([]string, 0, len(res.Chunks))
			for _, c := range res.Chunks {
				contents = append(contents, c.ChunkContent)
			}
			require.Equal(t, "billing page,refund page", chunkContents(contents), "the matching section, not the first one")
			stats := searcher.Stats()
			require.Equal(t, uint64(1), stats.Fallbacks[tc.reason], "the skipped planner is recorded by cause")
			require.Equal(t, uint64(1), stats.FallbackMatched)
			require.Zero(t, stats.FallbackFirstSection)
			require.Zero(t, stats.Planned)
		})
	}
}

// TestDegradedSearchWithoutMatchKeepsTheFirstSection pins the last resort: with
// no matching section, the fallback still returns the first section's pages.
func TestDegradedSearchWithoutMatchKeepsTheFirstSection(t *testing.T) {
	store := NewSysStore(newMemoryFS())
	putSearchDoc(t, store, "p", "/manual.pdf", manualTree("manual"))
	searcher := NewSearcher(errLLM{err: errors.New("down")}, store, &Indexer{}, defaultTestSettings())

	res, err := searcher.Run(context.Background(), SearchInput{Project: "p", Query: "zebra quokka", Limit: 5})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1)
	require.Equal(t, "intro page", res.Chunks[0].ChunkContent)
	require.Equal(t, uint64(1), searcher.Stats().FallbackFirstSection)
}

// TestDegradedMarkdownSearchUsesSectionLines covers Markdown trees, whose
// sections are keyed by heading line rather than page range: the first-section
// fallback asked for page 1, so a document that does not start with a heading
// returned nothing.
func TestDegradedMarkdownSearchUsesSectionLines(t *testing.T) {
	llm := &countingLLM{}
	idx := newMarkdownTestIndexer(t, defaultTestSettings(), llm)
	doc := "Preface without a heading.\n\n## Alpha\n\nAlpha body text.\n\n## Billing cycle\n\nInvoices go out monthly.\n"
	tree, _, err := idx.Index(context.Background(), KindMarkdown, []byte(doc), IndexOptions{DocID: "md"}, nil)
	require.NoError(t, err)
	tree.DocID = "md"
	tree.Type = KindMarkdown
	store := NewSysStore(newMemoryFS())
	putSearchDoc(t, store, "p", "/doc.md", tree)
	searcher := NewSearcher(errLLM{err: errors.New("down")}, store, idx, defaultTestSettings())

	res, err := searcher.Run(context.Background(), SearchInput{Project: "p", Query: "billing", Limit: 5})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1)
	require.Contains(t, res.Chunks[0].ChunkContent, "Invoices go out monthly.")

	res, err = searcher.Run(context.Background(), SearchInput{Project: "p", Query: "zebra", Limit: 5})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1, "the first section is found by its heading line")
	require.Contains(t, res.Chunks[0].ChunkContent, "Alpha body text.")
}

// plannerByTitleLLM fails the planner prompt of trees holding failTitle and
// answers every other planner prompt with page 2.
type plannerByTitleLLM struct{ failTitle string }

// Respond answers the planner prompt.
func (p plannerByTitleLLM) Respond(_ context.Context, req Request) (*Response, error) {
	for _, item := range req.Input {
		if strings.Contains(item.Content, p.failTitle) {
			return nil, errors.New("planner unavailable")
		}
	}
	return &Response{Text: `{"ranges":[{"start":2,"end":2,"reason":"install"}]}`}, nil
}

// CountTokens approximates the prompt size.
func (plannerByTitleLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, item := range req.Input {
		total += len(item.Content) / 4
	}
	return total, nil
}

// TestPlannedResultsOutrankFallbackResults checks the merge across candidates: a
// fallback result is a guess, so it must not displace a section the planner chose
// for another document.
func TestPlannedResultsOutrankFallbackResults(t *testing.T) {
	store := NewSysStore(newMemoryFS())
	guessed := manualTree("guessed")
	guessed.Structure[1].Title = "Installation guide (unplannable)"
	putSearchDoc(t, store, "p", "/a-guessed.pdf", guessed)
	putSearchDoc(t, store, "p", "/b-planned.pdf", manualTree("planned"))
	cfg := defaultTestSettings()
	// The failed planner request is charged its whole reservation, so the search
	// budget must hold two of them for the second candidate to be planned.
	cfg.TreeQuery.MaxTokens = 100_000
	searcher := NewSearcher(plannerByTitleLLM{failTitle: "unplannable"}, store, &Indexer{}, cfg)

	res, err := searcher.Run(context.Background(), SearchInput{Project: "p", Query: "installation", Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1)
	require.Equal(t, "/b-planned.pdf", res.Chunks[0].FilePath, "the planned section ranks first")
	stats := searcher.Stats()
	require.Equal(t, uint64(1), stats.Planned)
	require.Equal(t, uint64(1), stats.FallbackMatched)
}

// TestSearchQueryTermsAreBounded checks the fallback's matching rules: terms are
// case-folded words, CJK runs match by character pairs, stop words and one-letter
// words are ignored, and a long query is cut to a fixed number of terms.
func TestSearchQueryTermsAreBounded(t *testing.T) {
	terms := searchTerms("The Billing of INVOICES, a 2026 plan", maxSearchQueryTerms)
	require.ElementsMatch(t, []string{"billing", "invoices", "2026", "plan"}, keys(terms))
	require.ElementsMatch(t, []string{"账单", "单周", "周期"}, keys(searchTerms("账单周期", maxSearchQueryTerms)))
	require.ElementsMatch(t, []string{"税"}, keys(searchTerms("税", maxSearchQueryTerms)))
	long := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega ", 4)
	long += "unique1 unique2 unique3 unique4 unique5 unique6 unique7 unique8 unique9 unique10 unique11"
	require.Len(t, searchTerms(long, maxSearchQueryTerms), maxSearchQueryTerms)
}

// keys returns the keys of a term set.
func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// TestPageContentBoundsPlannerRanges checks that a range the planner returns,
// which the document's content can steer, costs no more than the tree's pages:
// resolving it iterated every page number of the range, so an end near
// math.MaxInt never returned.
func TestPageContentBoundsPlannerRanges(t *testing.T) {
	tree := manualTree("manual")
	done := make(chan []Chunk, 1)
	go func() {
		chunks, err := (&Indexer{}).GetPageContent(tree, []PageRange{{Start: 4, End: math.MaxInt}, {Start: -5, End: 1}, {Start: math.MaxInt - 1, End: math.MaxInt}})
		if err == nil {
			done <- chunks
		}
		close(done)
	}()
	select {
	case chunks := <-done:
		pages := make([]int, 0, len(chunks))
		for _, c := range chunks {
			pages = append(pages, c.Page)
		}
		require.Equal(t, []int{1, 4, 5}, pages)
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a huge page range must not iterate every page number")
	}
}
