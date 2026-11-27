package pageindex

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// The tests below reproduce the findings of an independent review of the fixes
// for the third maintainer review of #96.

// TestRollingRefusedPlannerIsCountedAsBudgetExhausted covers review finding 1:
// the planner's cheap pre-check reduced a rolling-budget refusal to a plain
// ErrBudgetExceeded, so the usual exhausted-tenant fallback was counted as
// search_limit and budget_exhausted was almost never recorded.
func TestRollingRefusedPlannerIsCountedAsBudgetExhausted(t *testing.T) {
	store := NewSysStore(newMemoryFS())
	putSearchDoc(t, store, "p", "/a.pdf", manualTree("a"))
	putSearchDoc(t, store, "p", "/b.pdf", manualTree("b"))
	cfg := defaultTestSettings()
	cfg.Limits = LimitsSettings{TenantLLMCallsPerWindow: 1}
	cfg.TreeQuery.MaxTokens = 100_000
	idx, err := NewIndexer(Deps{LLM: &countingLLM{}, Tokenizer: &fatTokenizer{perCall: 1}, Settings: cfg, Spend: frozenSpend(cfg.Limits)})
	require.NoError(t, err)
	searcher := NewSearcher(&countingLLM{}, store, idx, cfg)

	_, err = searcher.Run(context.Background(), SearchInput{Catalogs: []SearchCatalog{{Storage: "p"}}, Query: "billing", Limit: 5, Tenant: "t"})
	require.NoError(t, err)
	stats := searcher.Stats()
	require.Equal(t, uint64(1), stats.Planned)
	require.Equal(t, map[string]uint64{searchFallbackBudget: 1}, stats.Fallbacks, "the tenant's rolling budget refused the second planner")
}

// plannedPagesLLM answers every planner prompt with pages 1 to 3, and fails
// those whose tree holds failTitle.
type plannedPagesLLM struct{ plannerByTitleLLM }

// Respond answers the planner prompt with three pages.
func (p plannedPagesLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	if _, err := p.plannerByTitleLLM.Respond(ctx, req); err != nil {
		return nil, err
	}
	return &Response{Text: `{"ranges":[{"start":1,"end":3,"reason":"all"}]}`}, nil
}

// TestFallbackNeverOutranksAPlannedResult covers review finding 2: fallback
// scores were a multiple of the position decay, so a guess's first page tied a
// planned section's second page and beat its third.
func TestFallbackNeverOutranksAPlannedResult(t *testing.T) {
	store := NewSysStore(newMemoryFS())
	guessed := manualTree("guessed")
	guessed.Structure[1].Title = "Installation guide (unplannable)"
	putSearchDoc(t, store, "p", "/a-guessed.pdf", guessed)
	putSearchDoc(t, store, "p", "/b-planned.pdf", manualTree("planned"))
	cfg := defaultTestSettings()
	cfg.TreeQuery.MaxTokens = 100_000
	searcher := NewSearcher(plannedPagesLLM{plannerByTitleLLM{failTitle: "unplannable"}}, store, &Indexer{}, cfg)

	res, err := searcher.Run(context.Background(), SearchInput{Catalogs: []SearchCatalog{{Storage: "p"}}, Query: "installation", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 5)
	for i, c := range res.Chunks[:3] {
		require.Equal(t, "/b-planned.pdf", c.FilePath, "planned page %d ranks before every guess", i+1)
	}
	for _, c := range res.Chunks[3:] {
		require.Equal(t, "/a-guessed.pdf", c.FilePath)
		require.Less(t, c.Score, res.Chunks[2].Score, "a guess scores below the lowest planned page")
	}
}

// TestFallbackRanksTheBestMatchFirst covers review finding 3: the chunks of a
// fallback were re-sorted by page, so a weak match on an early page outranked the
// best match on a later page.
func TestFallbackRanksTheBestMatchFirst(t *testing.T) {
	store := NewSysStore(newMemoryFS())
	putSearchDoc(t, store, "p", "/doc.pdf", &Tree{
		DocID: "doc", Type: KindPDF, PageCount: 4,
		Structure: []*Node{
			{Title: "Overview", StartIndex: 1, EndIndex: 3, Summary: "Mentions refunds once."},
			{Title: "Invoices and refunds", StartIndex: 4, EndIndex: 4, Summary: "How invoices are refunded."},
		},
		Pages: []Page{{Page: 1, Content: "p1"}, {Page: 2, Content: "p2"}, {Page: 3, Content: "p3"}, {Page: 4, Content: "p4"}},
	})
	searcher := NewSearcher(errLLM{err: errAnswerFailed}, store, &Indexer{}, defaultTestSettings())

	res, err := searcher.Run(context.Background(), SearchInput{Catalogs: []SearchCatalog{{Storage: "p"}}, Query: "invoices refunds", Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Chunks, 1)
	require.Equal(t, "p4", res.Chunks[0].ChunkContent, "the section matching both terms in its title ranks first")
}

// errAnswerFailed is a provider failure for fake models.
var errAnswerFailed = errors.New("planner unavailable")

// incompleteLLM answers every prompt with an incomplete answer, like a provider
// that returned status failed or a content-filter stop.
type incompleteLLM struct{ countingLLM }

// Respond answers like countingLLM, marked incomplete.
func (l *incompleteLLM) Respond(ctx context.Context, req Request) (*Response, error) {
	resp, err := l.countingLLM.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Incomplete = true
	resp.Usage = Usage{InputTokens: 50, OutputTokens: 5, TotalTokens: 55}
	return resp, nil
}

// TestIncompleteAnswerIsAProviderError covers review finding 4: an answer the
// provider did not complete was classified like a refusal by the document's own
// ceilings, so the tree was marked with the final document_limit (never
// refreshed), and a search counted it as search_limit. It is a provider error:
// refreshable in indexing (file_search_file_summaries §4.6) and planner_failed in
// search.
func TestIncompleteAnswerIsAProviderError(t *testing.T) {
	cfg := summarizingSettings()
	llm := &incompleteLLM{}
	idx := newMarkdownTestIndexer(t, cfg, llm)
	_, stats, err := idx.Index(context.Background(), KindMarkdown, []byte(flatMarkdown(2, "i")), IndexOptions{DocID: "inc"}, nil)
	require.NoError(t, err, "an incomplete answer degrades its leaf instead of failing the run")
	require.Zero(t, stats.BudgetRefusals, "no budget refused anything")
	require.Equal(t, 2, stats.IncompleteAnswers)
	require.Equal(t, 2, stats.LocalSummaries)
	require.Equal(t, degradedProviderError, stats.degradedReason())
	require.True(t, refreshable(degradedProviderError), "a provider error is retried later")

	store := NewSysStore(newMemoryFS())
	putSearchDoc(t, store, "p", "/manual.pdf", manualTree("manual"))
	searcher := NewSearcher(&incompleteLLM{}, store, &Indexer{}, defaultTestSettings())
	_, err = searcher.Run(context.Background(), SearchInput{Catalogs: []SearchCatalog{{Storage: "p"}}, Query: "billing", Limit: 5})
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{searchFallbackPlannerFailed: 1}, searcher.Stats().Fallbacks)
}

// TestRollingBudgetSaturatesHugeCharges covers review finding 5: ledger sums were
// unchecked int64 additions, so a gateway that reported absurd usage wrapped the
// ledgers negative and the budget admitted again.
func TestRollingBudgetSaturatesHugeCharges(t *testing.T) {
	g, _ := newTestGovernor(LimitsSettings{Window: time.Hour})
	tickets := make([]*spendTicket, 0, 3)
	for range 3 {
		ticket, ok := g.admit("t", 1)
		require.True(t, ok)
		tickets = append(tickets, ticket)
	}
	for _, ticket := range tickets {
		ticket.settle(math.MaxInt64 / 2)
	}
	_, ok := g.admit("t", 1)
	require.False(t, ok, "a budget charged beyond any ceiling stays spent")
	_, ok = g.admit("other", 1)
	require.False(t, ok, "and so does the global budget")

	budget := newOperationBudget(100, 10, unlimited, "t")
	for range 3 {
		_, ok := budget.reserveUpTo(1, 10, 1)
		require.True(t, ok || budget.Remaining() <= 0)
		budget.settle(11, math.MaxInt64/2)
	}
	require.Negative(t, budget.Remaining(), "the operation budget saturates instead of wrapping")
}

// TestRequestsThatNeverLeftAreNotChargedOrCounted covers review finding 6: a
// request that failed before anything was sent (an invalid output schema, or a
// context already ended) was charged its whole reservation and counted as an
// upstream request.
func TestRequestsThatNeverLeftAreNotChargedOrCounted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	client, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(3), Tokenizer: tok})
	require.NoError(t, err)
	gate := newLLMGate(tok, frozenSpend(LimitsSettings{}), LimitsSettings{})

	for name, tc := range map[string]struct {
		ctx func() context.Context
		req Request
	}{
		"invalid schema": {ctx: context.Background, req: Request{Input: userInput("hi"), Schema: json.RawMessage(`{not json`)}},
		"ended context": {ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, req: Request{Input: userInput("hi")}},
	} {
		t.Run(name, func(t *testing.T) {
			stats := &Stats{}
			budget := newOperationBudget(100_000, 10, unlimited, "t").metered(stats)
			_, err := gate.call(tc.ctx(), client, tc.req, budget)
			require.Error(t, err)
			require.Equal(t, int64(100_000), budget.Remaining(), "nothing was sent, so nothing is charged")
			require.Zero(t, stats.UpstreamRequests)
			require.Zero(t, stats.EstimatedTokens)
		})
	}
	require.Zero(t, hits.Load())
}

// TestContextEndingDuringBackoffChargesTheLastRequest covers the second
// independent review: when the context ended while the client waited to retry,
// retry-go returned the context's error, and the request that had been answered
// with a 500 was charged its whole reservation instead of its input estimate.
func TestContextEndingDuringBackoffChargesTheLastRequest(t *testing.T) {
	srv, hits := statusServer(t, http.StatusInternalServerError)
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	client, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Tokenizer: tok,
		Retry: RetrySettings{MaxAttempts: 3, InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Second}})
	require.NoError(t, err)
	gate := newLLMGate(tok, frozenSpend(LimitsSettings{}), LimitsSettings{})
	req := Request{Input: userInput("Summarize the section.")}
	stats := &Stats{}
	budget := newOperationBudget(100_000, 10, unlimited, "t").metered(stats)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err = gate.call(ctx, client, req, budget)
	require.Error(t, err)
	require.Equal(t, int32(1), hits.Load())
	require.Equal(t, estimateInputTokens(tok, req), 100_000-budget.Remaining(), "the 500 is charged its input, as without the backoff")
	require.Equal(t, 1, stats.UpstreamRequests)
}

// TestLimiterRefusalIsFreeAndDegradesToTimeout covers the client-side rate
// limiter: a wait that would outlive the deadline sent nothing but used a call
// of the budgets, was retried, and aborted the indexing run instead of degrading
// it like the deadline it anticipates.
func TestLimiterRefusalIsFreeAndDegradesToTimeout(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesBody("completed", "null", "A generated summary.", usageJSON(40, 10, 50), 0)))
	}))
	t.Cleanup(srv.Close)
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	client, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Tokenizer: tok, Retry: fastRetry(3),
		Limiter: rate.NewLimiter(rate.Every(time.Hour), 1)})
	require.NoError(t, err)
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{TenantLLMCallsPerWindow: 2}
	spend := frozenSpend(cfg.Limits)
	idx, err := NewIndexer(Deps{LLM: client, Tokenizer: tok, Settings: cfg, Spend: spend})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	tree, stats, err := idx.Index(ctx, KindMarkdown, []byte(flatMarkdown(3, "l")), IndexOptions{DocID: "lim", Tenant: "t"}, nil)
	require.NoError(t, err, "a request the limiter cannot send in time degrades the run")
	require.Len(t, tree.Structure, 3)
	require.Equal(t, int32(1), hits.Load(), "only the first request was sent")
	require.Equal(t, 1, stats.UpstreamRequests)
	require.Equal(t, degradedTimeout, stats.degradedReason())
	require.Equal(t, 2, stats.LocalSummaries)
	require.False(t, spend.wouldRefuse("t"), "requests that were never sent did not use the tenant's calls")
}

// TestUnsentRequestsDoNotUseCalls checks the call side of finding 6: a request
// that never left the process returns its call to the operation and the rolling
// budgets, not only its tokens.
func TestUnsentRequestsDoNotUseCalls(t *testing.T) {
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	client, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: "http://127.0.0.1:1", Retry: fastRetry(3), Tokenizer: tok})
	require.NoError(t, err)
	spend := frozenSpend(LimitsSettings{TenantLLMCallsPerWindow: 1})
	gate := newLLMGate(tok, spend, LimitsSettings{})
	budget := newOperationBudget(100_000, 1, unlimited, "t")
	for range 3 {
		_, err := gate.call(context.Background(), client, Request{Input: userInput("hi"), Schema: json.RawMessage(`{not json`)}, budget)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrBudgetExceeded, "the call is not refused: the budgets still hold it")
	}
	require.False(t, budget.exhausted())
	require.False(t, spend.wouldRefuse("t"))
}
