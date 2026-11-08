package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// countingLLM is a fake upstream model that counts every call that reaches it,
// records the prompts, and answers each prompt kind with a canned response.
type countingLLM struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	usage   Usage
	// tocEntries is how many entries a table-of-contents prompt returns.
	tocEntries int
}

// Respond records the call and answers by prompt kind.
func (c *countingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := ""
	for _, item := range req.Input {
		prompt += item.Content
	}
	c.mu.Lock()
	c.calls++
	c.prompts = append(c.prompts, prompt)
	usage := c.usage
	entries := c.tocEntries
	c.mu.Unlock()

	text := "A generated summary of the section."
	switch {
	case strings.Contains(prompt, "detect if there is a table of content"):
		text = `{"thinking":"none","toc_detected":"no"}`
	case strings.Contains(prompt, "generate the tree structure of the document"):
		items := make([]map[string]any, 0, entries)
		for i := 0; i < entries; i++ {
			items = append(items, map[string]any{"structure": fmt.Sprintf("%d", i+1), "title": fmt.Sprintf("Section %d", i), "physical_index": fmt.Sprintf("<physical_index_%d>", i%3+1)})
		}
		body, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		text = string(body)
	case strings.Contains(prompt, "check if the given section appears or starts"):
		text = `{"thinking":"ok","answer":"yes"}`
	case strings.Contains(prompt, "document-retrieval planner"):
		text = `{"ranges":[{"start":1,"end":1,"reason":"first"}]}`
	}
	return &Response{Text: text, Usage: usage}, nil
}

// CountTokens approximates the prompt size.
func (c *countingLLM) CountTokens(_ context.Context, req Request) (int, error) {
	total := 0
	for _, item := range req.Input {
		total += len(item.Content) / 4
	}
	return total, nil
}

// Calls returns how many calls reached the fake model.
func (c *countingLLM) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Prompts returns a copy of every prompt the fake model received.
func (c *countingLLM) Prompts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts...)
}

// flatMarkdown returns a document with n unique same-level sections, the shape
// issue #85 uses to turn one write into n leaf summaries.
func flatMarkdown(n int, salt string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "## Section %s %d\n\nUnique body %s %d explains one topic.\n\n", salt, i, salt, i)
	}
	return b.String()
}

// summarizingSettings enables leaf summaries with the production algorithm knobs.
func summarizingSettings() Settings {
	cfg := defaultTestSettings()
	cfg.Algo.MaxTokenNumEachNode = 20_000
	cfg.Algo.GenerateNodeSummary = true
	return cfg
}

// TestMarkdownSummaryCallsDoNotScaleWithHeadings reproduces issue #85: each
// same-level heading became its own leaf and every leaf cost one upstream call,
// so the caller chose the bill. Doubling the headings past the per-document cap
// must not add a single call, and every leaf must still carry a summary.
func TestMarkdownSummaryCallsDoNotScaleWithHeadings(t *testing.T) {
	callsFor := func(leaves int) (int, *Tree) {
		llm := &countingLLM{}
		idx := newMarkdownTestIndexer(t, summarizingSettings(), llm)
		tree, _, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(leaves, "x")), IndexOptions{DocID: "flat"}, nil)
		require.NoError(t, err, "a document over the call cap must still index")
		return llm.Calls(), tree
	}

	smallCalls, _ := callsFor(600)
	largeCalls, tree := callsFor(1200)
	t.Logf("600 leaves: %d calls, 1200 leaves: %d calls", smallCalls, largeCalls)
	require.Less(t, smallCalls, 600, "a per-document cap must stop one call per leaf")
	require.Equal(t, smallCalls, largeCalls, "upstream calls must not grow with caller-chosen heading count")
	WalkNodes(tree.Structure, func(n *Node) {
		if len(n.Children) == 0 {
			require.NotEmpty(t, n.Summary, "leaves past the cap get a deterministic local summary")
		}
	})
}

// TestMarkdownTokenBudgetDoesNotScaleWithRoots reproduces the second half of
// issue #85: the token budget was seeded with max_token_num_each_node times the
// root count, so more roots bought more spend.
func TestMarkdownTokenBudgetDoesNotScaleWithRoots(t *testing.T) {
	tokensFor := func(roots int) int {
		llm := &countingLLM{usage: Usage{InputTokens: 9_000, OutputTokens: 1_000, TotalTokens: 10_000}}
		idx := newMarkdownTestIndexer(t, summarizingSettings(), llm)
		_, stats, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(roots, "y")), IndexOptions{DocID: "roots"}, nil)
		require.NoError(t, err)
		return stats.InputTokens + stats.OutputTokens
	}

	small := tokensFor(400)
	large := tokensFor(800)
	t.Logf("400 roots: %d tokens, 800 roots: %d tokens", small, large)
	require.Equal(t, small, large, "the token ceiling must be absolute, not proportional to the root count")
	require.LessOrEqual(t, large, int(DefaultMaxTokensPerDocument)+10_000, "spend stops at the absolute ceiling, overshooting by at most one call")
}

// TestPDFSummaryCallsDoNotScaleWithTOCEntries sweeps the PDF pipeline for the
// same defect: an LLM-extracted table of contents with many entries became many
// leaves and one summary call each.
func TestPDFSummaryCallsDoNotScaleWithTOCEntries(t *testing.T) {
	callsFor := func(entries int) int {
		llm := &countingLLM{tocEntries: entries}
		cfg := summarizingSettings()
		tok, err := NewTokenizer("gpt-5.4-mini")
		require.NoError(t, err)
		idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"one", "two", "three"}}})
		require.NoError(t, err)
		_, _, err = idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "pdf"}, nil)
		require.NoError(t, err)
		return llm.Calls()
	}

	small := callsFor(600)
	large := callsFor(1200)
	t.Logf("600 entries: %d calls, 1200 entries: %d calls", small, large)
	require.Less(t, small, 600)
	require.Equal(t, small, large, "PDF calls must not grow with the extracted table of contents")
}

// fixedPagesPDF is a PDF parser stub that returns fixed page text.
type fixedPagesPDF struct{ pages []string }

// PageCount returns the number of fixed pages.
func (f fixedPagesPDF) PageCount(context.Context, []byte) (int, error) { return len(f.pages), nil }

// PageText returns one fixed page.
func (f fixedPagesPDF) PageText(_ context.Context, _ []byte, page int) (string, error) {
	return f.pages[page-1], nil
}

// PagesText returns every fixed page.
func (f fixedPagesPDF) PagesText(context.Context, []byte) ([]string, error) {
	return append([]string(nil), f.pages...), nil
}

// Outline returns no bookmarks.
func (fixedPagesPDF) Outline(context.Context, []byte) ([]Bookmark, error) { return nil, nil }

// TestSearchPromptStaysWithinTreeQueryBudget sweeps the retrieval loop: the
// planner prompt embedded the whole outline, so a document with many long
// headings sent one upstream call far larger than tree_query.max_tokens.
func TestSearchPromptStaysWithinTreeQueryBudget(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	nodes := make([]*Node, 0, 2000)
	for i := 0; i < 2000; i++ {
		nodes = append(nodes, &Node{NodeID: fmt.Sprintf("%04d", i+1), Title: fmt.Sprintf("Heading %d %s", i, strings.Repeat("words ", 20)), LineNum: i + 1, Summary: strings.Repeat("summary ", 20)})
	}
	tree := &Tree{DocID: "big", Type: KindMarkdown, Structure: nodes, Pages: []Page{{Page: 1, Content: "first page"}}}
	require.NoError(t, store.PutTree(ctx, "p", "big", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/big.md", IndexEntry{DocID: "big", Type: "md"}))

	llm := &countingLLM{}
	cfg := defaultTestSettings()
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg})
	require.NoError(t, err)
	searcher := NewSearcher(llm, store, idx, cfg)
	res, err := searcher.Run(ctx, SearchInput{Project: "p", Query: "where?", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "an oversized outline must degrade, not break retrieval")
	require.Positive(t, llm.Calls(), "the planner still runs on a reduced outline that fits the budget")
	for _, prompt := range llm.Prompts() {
		require.LessOrEqual(t, tok.Count(prompt), cfg.TreeQuery.MaxTokens, "no planner call may exceed tree_query.max_tokens")
	}
}

// TestTenantBudgetHoldsAcrossRepeatedWrites reproduces the amplification across
// requests: without a rolling tenant budget, every write could spend a fresh
// per-document allowance. Writes must keep succeeding, one tenant's spend must
// stop growing, another tenant must keep its own budget, and searches must draw
// from the same budget.
func TestTenantBudgetHoldsAcrossRepeatedWrites(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	settings.Algo.GenerateNodeSummary = true
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	llm := &countingLLM{}
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: llm, Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	ctx := context.Background()
	tenantA := files.AuthContext{APIKey: "key-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	tenantB := files.AuthContext{APIKey: "key-b", APIKeyHash: "tenant-b", UserIdentity: "user:b"}

	write := func(auth files.AuthContext, i int) {
		_, err := plugin.Write(ctx, auth, "project", fmt.Sprintf("/doc-%d.md", i), flatMarkdown(200, fmt.Sprintf("%s-%d", auth.APIKeyHash, i)), "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, err, "an exhausted budget must degrade summaries, not fail the write")
	}
	for i := 0; i < 6; i++ {
		write(tenantA, i)
	}
	afterSix := llm.Calls()
	for i := 6; i < 12; i++ {
		write(tenantA, i)
	}
	afterTwelve := llm.Calls()
	t.Logf("tenant A: %d calls after 6 writes, %d after 12", afterSix, afterTwelve)
	require.Less(t, afterSix, 6*200, "repeated writes must not each spend a fresh allowance")
	require.Equal(t, afterSix, afterTwelve, "an exhausted tenant must not reach the upstream model again")

	tree, err := plugin.store.GetTree(ctx, PageIndexStorageProject(tenantA, "project"), docIDFromAuth(tenantA, "project", "/doc-11.md"))
	require.NoError(t, err, "the tree is still built and stored when the budget is exhausted")
	WalkNodes(tree.Structure, func(n *Node) { require.NotEmpty(t, n.Summary) })

	_, err = plugin.Search(ctx, tenantA, "project", "topic", "", 5)
	require.NoError(t, err)
	require.Equal(t, afterTwelve, llm.Calls(), "search draws from the same exhausted tenant budget")

	write(tenantB, 0)
	require.Greater(t, llm.Calls(), afterTwelve, "another tenant keeps its own budget")
}

// TestMarkdownLeavesPastCallCapGetLocalSummaries checks the degradation contract
// with a configured cap: leaves within the cap keep generated summaries, later
// leaves get deterministic local summaries, and the write still succeeds.
func TestMarkdownLeavesPastCallCapGetLocalSummaries(t *testing.T) {
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{MaxLLMCallsPerDocument: 3}
	llm := &countingLLM{}
	idx := newMarkdownTestIndexer(t, cfg, llm)
	tree, stats, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(10, "z")), IndexOptions{DocID: "cap"}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, llm.Calls())
	require.Equal(t, 3, stats.LLMCalls)
	require.Equal(t, 7, stats.LocalSummaries)
	for i, n := range tree.Structure {
		if i < 3 {
			require.Equal(t, "A generated summary of the section.", n.Summary)
			continue
		}
		require.Equal(t, fmt.Sprintf("Section z %d Unique body z %d explains one topic.", i, i), n.Summary)
	}
}

// TestMarkdownNodeCapKeepsContent checks the structural node ceiling: headings
// past the cap stay in the last section's text, so no content is lost.
func TestMarkdownNodeCapKeepsContent(t *testing.T) {
	cfg := defaultTestSettings()
	cfg.Limits = LimitsSettings{MaxNodesPerDocument: 10}
	idx := newMarkdownTestIndexer(t, cfg, &countingLLM{})
	tree, stats, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(50, "n")), IndexOptions{DocID: "nodes"}, nil)
	require.NoError(t, err)
	require.True(t, stats.StructureTruncated)
	require.Len(t, tree.Structure, 10)
	require.Contains(t, tree.Structure[9].Text, "Unique body n 49", "content past the cap stays retrievable")
}

// TestPDFBudgetExhaustionDegradesToDocumentNode checks that a PDF whose budget
// runs out during table-of-contents detection still gets a usable tree.
func TestPDFBudgetExhaustionDegradesToDocumentNode(t *testing.T) {
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{MaxLLMCallsPerDocument: 1}
	llm := &countingLLM{tocEntries: 5}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"alpha page", "beta page", "gamma page"}}})
	require.NoError(t, err)
	tree, stats, err := idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "pdf-degrade"}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, llm.Calls())
	require.Len(t, tree.Structure, 1)
	require.Equal(t, "Document", tree.Structure[0].Title)
	require.Equal(t, "alpha page beta page gamma page", tree.Structure[0].Summary)
	require.Equal(t, 1, stats.LocalSummaries)
}

// cancellingLLM cancels the run's context on its first call.
type cancellingLLM struct {
	countingLLM
	cancel context.CancelFunc
}

// Respond cancels the run, then answers.
func (c *cancellingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	c.cancel()
	return c.countingLLM.Respond(context.Background(), req)
}

// TestMarkdownSummariesStopWhenCanceled checks that summarization stops at the
// next leaf after the caller goes away, even when later leaves would be local.
func TestMarkdownSummariesStopWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llm := &cancellingLLM{cancel: cancel}
	idx := newMarkdownTestIndexer(t, summarizingSettings(), llm)
	_, _, err := idx.Index(ctx, KindMarkdown, []byte(flatMarkdown(50, "c")), IndexOptions{DocID: "cancel"}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, llm.Calls())
}

// blockingLLM holds every call until its context ends, like a stalled upstream.
type blockingLLM struct{ countingLLM }

// Respond blocks until ctx is done.
func (b *blockingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestWriteBoundsIndexingByTimeoutIndex checks the documented wall-time limit of
// synchronous indexing: a stalled upstream cannot hold the write past
// indexer.timeout_index, and the write still succeeds with a deterministic summary.
func TestWriteBoundsIndexingByTimeoutIndex(t *testing.T) {
	db, service := newPersistenceTestService(t)
	defer func() { require.NoError(t, db.Close()) }()
	settings := persistenceTestSettings()
	settings.Indexer.TimeoutIndex = 100 * time.Millisecond
	settings.Algo.GenerateNodeSummary = true
	tokenizer, err := NewTokenizer(settings.LLM.IndexingModel)
	require.NoError(t, err)
	systemFS, err := service.SystemNamespace(SysOwner)
	require.NoError(t, err)
	llm := &blockingLLM{}
	plugin, err := New(PluginDeps{UserFS: service, SystemFS: systemFS, Settings: settings, LLM: llm, Tokenizer: tokenizer})
	require.NoError(t, err)
	require.NoError(t, plugin.Start(context.Background()))
	defer func() { require.NoError(t, plugin.Stop(context.Background())) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth := files.AuthContext{APIKey: "key", APIKeyHash: "tenant", UserIdentity: "user:t"}

	start := time.Now()
	_, err = plugin.Write(ctx, auth, "project", "/stalled.md", "# Stalled\n\nThe upstream never answers.", "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err, "an indexing timeout degrades to the deterministic summary")
	require.Less(t, time.Since(start), 3*time.Second, "indexing must stop at indexer.timeout_index")
	require.Equal(t, 1, llm.Calls())
	read, err := plugin.Read(ctx, auth, "project", "/stalled.md", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "# Stalled\n\nThe upstream never answers.", read.Content)
}
