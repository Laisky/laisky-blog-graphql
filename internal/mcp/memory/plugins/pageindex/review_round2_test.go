package pageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestSlowCallSettlesAtCompletionTime covers finding A of the second review of
// #96: a slow call's charge must not be cancelled by the time it spent in
// flight. A call admitted at 1 token and settled a whole window later at 100
// tokens fills the window; once its admission slot leaves the window, the
// 99-token debt, which counts from its settlement, still holds and exactly 1
// token remains.
func TestSlowCallSettlesAtCompletionTime(t *testing.T) {
	for name, limits := range map[string]LimitsSettings{
		"tenant budget": {TenantTokensPerWindow: 100, Window: time.Hour},
		"global budget": {GlobalTokensPerWindow: 100, Window: time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			g, now := newTestGovernor(limits)
			ticket, ok := g.admit("t", 1)
			require.True(t, ok)
			*now = now.Add(time.Hour)
			ticket.settle(100)
			require.True(t, g.wouldRefuse("t"), "the slow call's whole charge fills the window")
			*now = now.Add(time.Hour / spendSlots)
			_, ok = g.admit("t", 2)
			require.False(t, ok, "the debt is charged at completion time, so only 1 token remains")
			_, ok = g.admit("t", 1)
			require.True(t, ok, "the budget holds exactly the token the debt left")
		})
	}
}

// TestStructureTruncationIsPublishedAsDocumentLimit covers finding B: a tree cut
// short only by the node ceiling (no LLM call refused) was published as ready.
// The tree, the catalog entry and the user-row summary must all report
// document_limit, and the entry is final because a refresh hits the same ceiling.
func TestStructureTruncationIsPublishedAsDocumentLimit(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	h := newPluginHarness(t, &countingLLM{}, LimitsSettings{MaxNodesPerDocument: 3}, clock.Now)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "sk-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	_, err := h.plugin.Write(ctx, auth, "p", "/doc.md", flatMarkdown(10, "t"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	require.Equal(t, 3, summaryPrompts(h.llm), "every kept leaf is summarized, so no call was refused")

	status, code := h.summaryStatus(t, auth, "p", "/doc.md")
	require.Equal(t, string(files.SummaryStatusDegraded), status, "a truncated tree is not ready")
	require.Equal(t, degradedDocumentLimit, code)
	entry := h.entry(t, auth, "p", "/doc.md")
	require.Equal(t, degradedDocumentLimit, entry.Degraded)
	require.Empty(t, entry.RefreshAfter, "a structural limit is final for this content")
	tree, err := h.plugin.store.GetTree(ctx, PageIndexStorageProject(auth, "p"), entry.DocID)
	require.NoError(t, err)
	require.Equal(t, degradedDocumentLimit, tree.Degraded)
	require.Len(t, tree.Structure, 3)
	require.Contains(t, tree.Structure[2].Text, "Unique body t 9", "content past the cap stays in the last section")

	res, err := h.plugin.Search(ctx, auth, "p", "topic", "", 5)
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks, "a degraded tree stays searchable")
	for _, chunk := range res.Chunks {
		require.NotEmpty(t, chunk.FileSummary, "every hit of a degraded tree carries its bounded summary")
	}
	status, code = h.summaryStatus(t, auth, "p", "/doc.md")
	require.Equal(t, string(files.SummaryStatusDegraded), status, "searching never presents the tree as complete")
	require.Equal(t, degradedDocumentLimit, code)
}

// TestRefreshedTruncatedTreeSettlesOnDocumentLimit checks the precedence rule end
// to end: a tree both truncated and short of rolling budget is published as
// budget_exhausted so a refresh can recover its summaries, and the refreshed tree,
// still truncated, settles on the final document_limit.
func TestRefreshedTruncatedTreeSettlesOnDocumentLimit(t *testing.T) {
	clock := &movableClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	h := newPluginHarness(t, &countingLLM{}, LimitsSettings{TenantLLMCallsPerWindow: 8, MaxNodesPerDocument: 5, Window: time.Hour}, clock.Now)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "sk-a", APIKeyHash: "tenant-a", UserIdentity: "user:a"}
	_, err := h.plugin.Write(ctx, auth, "p", "/first.md", flatMarkdown(5, "f"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, err = h.plugin.Write(ctx, auth, "p", "/doc.md", flatMarkdown(10, "d"), "utf-8", 0, files.WriteModeTruncate)
	require.NoError(t, err)
	_, code := h.summaryStatus(t, auth, "p", "/doc.md")
	require.Equal(t, degradedBudget, code, "the refreshable cause is reported over the structural limit")
	require.NotEmpty(t, h.entry(t, auth, "p", "/doc.md").RefreshAfter)

	clock.Advance(2 * time.Hour)
	_, err = h.plugin.Search(ctx, auth, "p", "topic", "", 5)
	require.NoError(t, err)
	// The summary row and the catalog entry are published one after the other.
	require.Eventually(t, func() bool {
		_, code := h.summaryStatus(t, auth, "p", "/doc.md")
		return code == degradedDocumentLimit && h.entry(t, auth, "p", "/doc.md").Degraded == degradedDocumentLimit
	}, 10*time.Second, 10*time.Millisecond, "the refreshed tree reports the permanent limit")
	entry := h.entry(t, auth, "p", "/doc.md")
	require.Equal(t, degradedDocumentLimit, entry.Degraded)
	require.Empty(t, entry.RefreshAfter, "and is final")
	require.False(t, refreshDue(entry, clock.Now().Add(24*time.Hour)))
}

// TestPDFStructureTruncationIsDegraded checks the PDF side of finding B: table-of-
// contents entries dropped by the node ceiling mark the run document_limit even
// when every remaining call is admitted.
func TestPDFStructureTruncationIsDegraded(t *testing.T) {
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{MaxNodesPerDocument: 10}
	llm := &countingLLM{tocEntries: 50}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"one", "two", "three"}}, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	_, stats, err := idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "pdf"}, nil)
	require.NoError(t, err)
	require.True(t, stats.StructureTruncated)
	require.Zero(t, stats.BudgetRefusals, "no call was refused")
	require.Equal(t, degradedDocumentLimit, stats.degradedReason())
}

// TestDegradedReasonPrecedence pins the classification: a refreshable cause wins
// over a structural limit so a refresh can still recover the refused work, and the
// refreshed tree then reports the permanent document_limit.
func TestDegradedReasonPrecedence(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*Stats)
		want string
	}{
		"complete":                      {func(*Stats) {}, ""},
		"truncated only":                {func(s *Stats) { s.StructureTruncated = true }, degradedDocumentLimit},
		"document ceiling":              {func(s *Stats) { s.BudgetRefusals, s.LocalSummaries = 1, 1 }, degradedDocumentLimit},
		"rolling refusal and truncated": {func(s *Stats) { s.BudgetRefusals, s.RollingRefusals, s.StructureTruncated = 1, 1, true }, degradedBudget},
		"deadline and truncated":        {func(s *Stats) { s.DeadlineReached, s.StructureTruncated = true, true }, degradedTimeout},
	} {
		stats := &Stats{}
		tc.set(stats)
		require.Equal(t, tc.want, stats.degradedReason(), name)
	}
}

// TestCachedResponsesAreFreeAfterExhaustion covers finding E: a leaf whose
// response is already cached was summarized locally once the rolling budget was
// spent, because the budget was checked before the cache. Re-indexing the same
// content must reuse every cached response without reaching the model.
func TestCachedResponsesAreFreeAfterExhaustion(t *testing.T) {
	cache, err := NewCache(CacheConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "cache.bbolt")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{TenantLLMCallsPerWindow: 5}
	llm := &countingLLM{}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Cache: cache, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	doc := []byte(flatMarkdown(5, "c"))

	_, first, err := idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "c", Tenant: "t"}, nil)
	require.NoError(t, err)
	require.Equal(t, 5, first.LLMCalls)
	require.Empty(t, first.degradedReason())

	tree, second, err := idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "c", Tenant: "t"}, nil)
	require.NoError(t, err)
	require.Equal(t, 5, llm.Calls(), "cached responses never reach the model")
	require.Equal(t, 5, second.Cached)
	require.Zero(t, second.LocalSummaries, "a cached response is used even when the budget is spent")
	require.Empty(t, second.degradedReason())
	for _, n := range tree.Structure {
		require.Equal(t, "A generated summary of the section.", n.Summary)
	}
}

// referenceTitleDocument returns a document the linear scanner handles (one line
// nests past maxMarkdownNesting) with defs link-reference definitions and
// headings headings; heading i uses the reference ref-i, which is defined only
// for i < defs.
func referenceTitleDocument(defs, headings int) []byte {
	var b strings.Builder
	b.WriteString(strings.Repeat("> ", maxMarkdownNesting+8) + "deep\n\n")
	for i := 0; i < defs; i++ {
		fmt.Fprintf(&b, "[ref-%d]: https://example.com/%d/%s\n", i, i, strings.Repeat("p", 100))
	}
	b.WriteString("\n")
	for i := 0; i < headings; i++ {
		fmt.Fprintf(&b, "## Heading %d [ref-%d]\n\nBody %d.\n\n", i, i, i)
	}
	return []byte(b.String())
}

// TestFallbackTitlesParseReferenceDefinitionsOnce covers finding D: the fallback
// title renderer appended every retained link-reference definition to each title
// that contains '[' and re-parsed them, so the cost grew with headings times
// definitions. Rendering must cost about the same with 512 definitions as with
// one, and reference links must still resolve.
func TestFallbackTitlesParseReferenceDefinitionsOnce(t *testing.T) {
	cost := func(defs int) (time.Duration, markdownHeadings) {
		var got markdownHeadings
		elapsed := minDuration(3, func() {
			var err error
			got, err = extractMarkdownHeadings(context.Background(), referenceTitleDocument(defs, DefaultMaxNodesPerDocument), DefaultMaxNodesPerDocument)
			require.NoError(t, err)
		})
		return elapsed, got
	}
	few, fewHeadings := cost(1)
	many, manyHeadings := cost(maxFallbackLinkRefs)
	t.Logf("titles with 1 definition: %v, with %d definitions: %v", few, maxFallbackLinkRefs, many)
	require.True(t, fewHeadings.fallback)
	require.True(t, manyHeadings.fallback)
	require.Len(t, manyHeadings.headers, DefaultMaxNodesPerDocument)
	require.Equal(t, "Heading 7 ref-7", manyHeadings.headers[7].Title, "a reference link renders as its label text")
	require.Equal(t, "Heading 600 [ref-600]", manyHeadings.headers[600].Title, "an undefined reference stays literal")
	require.Equal(t, "Heading 0 ref-0", fewHeadings.headers[0].Title)
	if !raceEnabled {
		require.LessOrEqual(t, many, 4*few+50*time.Millisecond, "title rendering must not scale with the definitions")
	}
}

// TestFallbackTitleRenderingPollsContext checks that the fallback scanner's final
// title-rendering loop stops when the context is canceled.
func TestFallbackTitleRenderingPollsContext(t *testing.T) {
	body := referenceTitleDocument(4, DefaultMaxNodesPerDocument)
	lines := strings.Count(string(body), "\n") + 1
	// The scan itself polls once per markdownPollEvery lines; cancel just after it.
	ctx := &countdownContext{Context: context.Background(), remaining: int64(lines/markdownPollEvery) + 1}
	_, err := scanMarkdownHeadingsLinear(ctx, body, DefaultMaxNodesPerDocument)
	require.ErrorIs(t, err, context.Canceled)
}

// verboseLLM is a provider that honors max_output_tokens but otherwise generates
// output tokens per answer, the shape of a document that induces long answers.
// It reports usage like the real API and records every request it receives.
type verboseLLM struct {
	countingLLM
	tok    Tokenizer
	output int
	mu     sync.Mutex
	maxOut []int
	// long records the output ceiling of every request that re-emits a table of
	// contents, short that of every other request.
	long, short []int
	billed      int64
}

// Respond answers like countingLLM and reports provider-side usage: the input
// counted by the model's tokenizer plus a few framing tokens per message.
func (v *verboseLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	resp, err := v.countingLLM.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	input := 0
	for _, item := range req.Input {
		input += v.tok.Count(item.Content) + 4
	}
	output := v.output
	if req.MaxOutTokens > 0 && req.MaxOutTokens < output {
		output = req.MaxOutTokens
	}
	resp.Usage = Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
	v.mu.Lock()
	v.maxOut = append(v.maxOut, req.MaxOutTokens)
	if req.LongOutput {
		v.long = append(v.long, req.MaxOutTokens)
	} else {
		v.short = append(v.short, req.MaxOutTokens)
	}
	v.billed += int64(input + output)
	v.mu.Unlock()
	return resp, nil
}

// Billed returns the provider-reported tokens of every request.
func (v *verboseLLM) Billed() int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.billed
}

// MaxOut returns the max_output_tokens of every request.
func (v *verboseLLM) MaxOut() []int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]int(nil), v.maxOut...)
}

// TestOutputTokensAreReservedBeforeDispatch covers finding C: only the input
// estimate was reserved and no request set max_output_tokens, so one answer could
// overshoot the per-document and rolling token ceilings by its whole output.
// Every request must carry an output ceiling, and the provider-reported spend
// must stay within the ceilings it was admitted against.
func TestOutputTokensAreReservedBeforeDispatch(t *testing.T) {
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{MaxTokensPerDocument: 200_000, TenantTokensPerWindow: 300_000}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	llm := &verboseLLM{tok: tok, output: 150_000}
	spend := frozenSpend(cfg.Limits)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: spend})
	require.NoError(t, err)

	_, stats, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(50, "v")), IndexOptions{DocID: "v1", Tenant: "t"}, nil)
	require.NoError(t, err)
	first := llm.Billed()
	t.Logf("first document: %d calls billed %d tokens", llm.Calls(), first)
	require.Positive(t, stats.LLMCalls)
	require.LessOrEqual(t, first, int64(200_000), "the per-document ceiling holds after the output is billed")

	_, _, err = idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(50, "w")), IndexOptions{DocID: "v2", Tenant: "t"}, nil)
	require.NoError(t, err)
	t.Logf("both documents: %d calls billed %d tokens", llm.Calls(), llm.Billed())
	require.LessOrEqual(t, llm.Billed(), int64(300_000), "the tenant window holds after the output is billed")
	for _, maxOut := range llm.MaxOut() {
		require.Positive(t, maxOut, "every request carries an explicit output ceiling")
		require.LessOrEqual(t, maxOut, DefaultMaxOutputTokensPerCall, "a summary is a short answer")
	}
}

// TestSearchPlannerReservesItsAnswer checks the search side of finding C: the
// planner's outline leaves its answer a reserve, so an outline that alone would
// fill tree_query.max_tokens is reduced, and the outline plus the output ceiling
// sent with it fits the budget.
func TestSearchPlannerReservesItsAnswer(t *testing.T) {
	cfg := defaultTestSettings()
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	budget := int64(cfg.TreeQuery.MaxTokens)
	// Grow a flat outline until its prompt alone nearly fills the budget.
	nodes := make([]*Node, 0, 128)
	for {
		nodes = append(nodes, &Node{NodeID: fmt.Sprintf("%04d", len(nodes)+1), Title: fmt.Sprintf("Heading %d about %s", len(nodes), strings.Repeat("words ", 6)), LineNum: len(nodes) + 1})
		body, err := json.Marshal(CloneOutline(nodes))
		require.NoError(t, err)
		prompt, err := RenderPrompt(PromptPickPageRanges, PickPageRangesVars{Query: "where?", Tree: string(body), MaxRanges: 5})
		require.NoError(t, err)
		if estimateInputTokens(tok, Request{Input: userInput(prompt)}) > budget-200 {
			break
		}
	}
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	tree := &Tree{DocID: "big", Type: KindMarkdown, Structure: nodes, Pages: []Page{{Page: 1, Content: "first page"}}}
	require.NoError(t, store.PutTree(ctx, "p", "big", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/big.md", IndexEntry{DocID: "big", Type: "md"}))

	llm := &verboseLLM{tok: tok, output: 150_000}
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	_, err = NewSearcher(llm, store, idx, cfg).Run(ctx, SearchInput{Project: "p", Query: "where?", Limit: 3})
	require.NoError(t, err)
	require.Equal(t, 1, llm.Calls(), "the planner runs on an outline that leaves room for its answer")
	maxOut := int64(llm.MaxOut()[0])
	require.GreaterOrEqual(t, maxOut, idx.gate.answerReserve(budget), "the answer gets at least its reserve")
	require.LessOrEqual(t, int64(tok.Count(llm.Prompts()[0]))+maxOut, budget)
	require.LessOrEqual(t, llm.Billed(), budget)
}

// TestOpenAILLMSendsOutputCeilingAndReportsTruncation checks the client side of
// finding C: the granted ceiling is sent as max_output_tokens, and an answer the
// provider cut at it is reported as truncated (marked incomplete, or using the
// whole ceiling) while a complete answer is not.
func TestOpenAILLMSendsOutputCeilingAndReportsTruncation(t *testing.T) {
	for name, tc := range map[string]struct {
		status, details       string
		outputTokens          int
		truncated, incomplete bool
	}{
		"marked incomplete":    {"incomplete", `{"reason":"max_output_tokens"}`, 200, true, false},
		"whole ceiling used":   {"completed", "null", 256, true, false},
		"complete answer":      {"completed", "null", 20, false, false},
		"other incompleteness": {"incomplete", `{"reason":"content_filter"}`, 20, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			var sent atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					MaxOutputTokens int64 `json:"max_output_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				sent.Store(body.MaxOutputTokens)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":0,"model":"gpt-5.4-mini","status":%q,"incomplete_details":%s,`+
					`"output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"partial answer","annotations":[]}]}],`+
					`"usage":{"input_tokens":10,"output_tokens":%d,"total_tokens":%d,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`,
					tc.status, tc.details, tc.outputTokens, 10+tc.outputTokens)
			}))
			t.Cleanup(srv.Close)
			llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(1)})
			require.NoError(t, err)
			resp, err := llm.Respond(context.Background(), Request{Input: userInput("hi"), MaxOutTokens: 256})
			require.NoError(t, err)
			require.Equal(t, int64(256), sent.Load(), "the ceiling is sent as max_output_tokens")
			require.Equal(t, tc.truncated, resp.Truncated)
			require.Equal(t, tc.incomplete, resp.Incomplete, "an answer incomplete for another reason is not usable either")
			require.Equal(t, 10+tc.outputTokens, resp.Usage.TotalTokens)
		})
	}
}

// truncatingLLM answers every request as cut at its output ceiling.
type truncatingLLM struct{ countingLLM }

// Respond answers like countingLLM, marked truncated and billed in full.
func (l *truncatingLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	resp, err := l.countingLLM.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Truncated = true
	resp.Usage = Usage{InputTokens: 100, OutputTokens: req.MaxOutTokens, TotalTokens: 100 + req.MaxOutTokens}
	return resp, nil
}

// TestTruncatedAnswerDegradesAndIsNotCached checks that an answer cut at its
// output ceiling is billed and counted, but neither used nor cached: the leaf is
// summarized locally, the tree reports document_limit, and a later run asks again.
func TestTruncatedAnswerDegradesAndIsNotCached(t *testing.T) {
	cache, err := NewCache(CacheConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "cache.bbolt")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	cfg := summarizingSettings()
	llm := &truncatingLLM{}
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Cache: cache, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	doc := []byte("# Only\n\nOne section body.\n")
	tree, stats, err := idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "cut"}, nil)
	require.NoError(t, err)
	require.Equal(t, "Only One section body.", tree.Structure[0].Summary, "a truncated answer is not used")
	require.Equal(t, 1, stats.LLMCalls, "the billed call is counted")
	require.Equal(t, 1, stats.LocalSummaries)
	require.Equal(t, degradedDocumentLimit, stats.degradedReason())
	_, _, err = idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "cut"}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, llm.Calls(), "a truncated answer is never cached")
}

// noUsageLLM answers without reporting usage, like a proxy that drops it.
type noUsageLLM struct{ text string }

// Respond returns text without usage.
func (l noUsageLLM) Respond(context.Context, Request) (*Response, error) {
	return &Response{Text: l.text}, nil
}

// CountTokens is unused.
func (noUsageLLM) CountTokens(context.Context, Request) (int, error) { return 0, nil }

// TestAttemptChargesFollowWhatTheProviderReported pins the settlement of every
// upstream request: reported usage when there is one; the input estimate plus the
// answer's estimated tokens without it; the input estimate for an error response;
// and the whole reservation for a request the provider never answered, which it
// may have processed and billed in full.
func TestAttemptChargesFollowWhatTheProviderReported(t *testing.T) {
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	req := Request{Input: userInput("Summarize this section of the handbook.")}
	gate := newLLMGate(tok, frozenSpend(LimitsSettings{}), LimitsSettings{})
	input := estimateInputTokens(tok, req)
	const total = 100_000
	spent := func(llm LLM) int64 {
		budget := newOperationBudget(total, 10, unlimited, "t")
		_, _ = gate.call(context.Background(), llm, req, budget)
		return total - budget.Remaining()
	}

	reported := &verboseLLM{tok: tok, output: 30}
	require.Equal(t, spent(reported), reported.Billed(), "reported usage is charged as reported")

	answer := "A short generated summary."
	require.Equal(t, input+int64(tok.Count(answer)), spent(noUsageLLM{text: answer}), "an answer without usage is estimated")

	srv, _ := statusServer(t, http.StatusServiceUnavailable)
	answered, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(1)})
	require.NoError(t, err)
	require.Equal(t, input, spent(answered), "an error response is charged its input")

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	unanswered, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: closed.URL, Retry: fastRetry(1)})
	require.NoError(t, err)
	require.Equal(t, input+DefaultMaxOutputTokensPerCall, spent(unanswered), "an unanswered request is charged its whole reservation")

	limited, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(1), Limiter: rate.NewLimiter(rate.Every(time.Hour), 0)})
	require.NoError(t, err)
	require.Zero(t, spent(limited), "a request the rate limiter kept in the process costs nothing")

	retried, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(3)})
	require.NoError(t, err)
	require.Equal(t, 3*input, spent(retried), "every failed attempt is charged its input")

	// A budget that holds one reservation plus the inputs of two failed attempts
	// admits both retries only when each failed attempt is settled before its
	// retry is reserved.
	tight, hits := statusServer(t, http.StatusServiceUnavailable)
	tightLLM, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: tight.URL, Retry: fastRetry(3)})
	require.NoError(t, err)
	budget := newOperationBudget(3*input+DefaultMaxOutputTokensPerCall, 10, unlimited, "t")
	_, err = gate.call(context.Background(), tightLLM, req, budget)
	require.Error(t, err)
	require.Equal(t, int32(3), hits.Load(), "an unused reservation is returned before the retry is reserved")
	require.Equal(t, int64(DefaultMaxOutputTokensPerCall), budget.Remaining())
}

// TestTOCPhasesGetTheLongOutputCeiling checks the phase split of finding C:
// answers that re-emit a table of contents may be long and get the larger
// ceiling, every other answer the short one.
func TestTOCPhasesGetTheLongOutputCeiling(t *testing.T) {
	cfg := summarizingSettings()
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	llm := &verboseLLM{tok: tok, output: 40}
	llm.tocEntries = 10
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, PDF: fixedPagesPDF{pages: []string{"one", "two", "three"}}, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	_, stats, err := idx.Index(context.Background(), KindPDF, []byte("%PDF"), IndexOptions{DocID: "phases"}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, llm.long)
	require.NotEmpty(t, llm.short)
	for _, maxOut := range llm.long {
		require.Greater(t, maxOut, DefaultMaxOutputTokensPerCall)
		require.LessOrEqual(t, maxOut, DefaultMaxTOCOutputTokensPerCall)
	}
	for _, maxOut := range llm.short {
		require.LessOrEqual(t, maxOut, DefaultMaxOutputTokensPerCall)
	}
	require.Zero(t, stats.BudgetRefusals, "concurrent short answers of a small PDF fit its budget")
	require.Empty(t, stats.degradedReason())
}
