package pageindex

import "sync"

// maxPreciseEstimateBytes is the largest prompt that is tokenized to estimate its
// cost. A larger prompt is estimated by its byte length, which bounds its token
// count from above, so estimating never costs more than tokenizing this much.
const maxPreciseEstimateBytes = 256 << 10

// llmGate is the single admission point every upstream LLM call passes through,
// for indexing and for search. It enforces the operation's absolute ceilings
// (calls and tokens, checked against an estimate before the call is sent) and
// the rolling tenant and global budgets of the SpendGovernor.
type llmGate struct {
	tok   Tokenizer
	spend *SpendGovernor
}

// llmAdmission is one admitted call and every retry of it admitted since. Each
// upstream request holds its own reservation, settled exactly once.
type llmAdmission struct {
	gate     llmGate
	budget   *Budget
	estimate int64
	mu       sync.Mutex
	tickets  []*spendTicket
}

// wouldRefuse reports, cheaply and without spending, that no call can be admitted
// for budget, so callers can skip rendering and estimating a prompt.
func (g llmGate) wouldRefuse(budget *Budget) bool {
	return g.precheck(budget) != nil
}

// precheck returns the refusal no call for budget could escape: ErrBudgetExceeded
// for the operation's own ceiling, errRollingBudgetExceeded for a rolling budget.
func (g llmGate) precheck(budget *Budget) error {
	if budget.exhausted() {
		return ErrBudgetExceeded
	}
	if g.spend.wouldRefuse(budget.Tenant()) {
		return errRollingBudgetExceeded
	}
	return nil
}

// admit reserves req's estimated input tokens and one call from budget and from the
// rolling budgets. It returns ErrBudgetExceeded (or errRollingBudgetExceeded,
// which wraps it), reserving nothing, when any of them would be exceeded.
func (g llmGate) admit(req Request, budget *Budget) (*llmAdmission, error) {
	if err := g.precheck(budget); err != nil {
		return nil, err
	}
	a := &llmAdmission{gate: g, budget: budget, estimate: estimateInputTokens(g.tok, req)}
	if err := a.admitOne(); err != nil {
		return nil, err
	}
	return a, nil
}

// admitOne reserves one more upstream request at the call's estimate.
func (a *llmAdmission) admitOne() error {
	if !a.budget.reserve(a.estimate) {
		return ErrBudgetExceeded
	}
	ticket, ok := a.gate.spend.admit(a.budget.Tenant(), a.estimate)
	if !ok {
		a.budget.release(a.estimate)
		return errRollingBudgetExceeded
	}
	a.mu.Lock()
	a.tickets = append(a.tickets, ticket)
	a.mu.Unlock()
	return nil
}

// bind returns req with AdmitRetry set, so every retry the client makes is
// admitted against the same budgets as a new call before it is sent.
func (a *llmAdmission) bind(req Request) Request {
	req.AdmitRetry = func(int) error { return a.admitOne() }
	return req
}

// settle charges every upstream request of the call: the last, which produced
// resp, at the provider-reported total when it reports one, and every earlier
// (failed) request, or a failed last one, at the input estimate, since the
// provider may have billed it.
func (a *llmAdmission) settle(resp *Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, ticket := range a.tickets {
		charged := a.estimate
		if i == len(a.tickets)-1 && resp != nil && resp.Usage.TotalTokens > 0 {
			charged = int64(resp.Usage.TotalTokens)
		}
		a.budget.settle(a.estimate, charged)
		ticket.settle(charged)
	}
	a.tickets = nil
}

// release returns an admission whose call was never sent: the operation budget is
// restored and the rolling budgets keep only the call.
func (a *llmAdmission) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ticket := range a.tickets {
		a.budget.release(a.estimate)
		ticket.settle(0)
	}
	a.tickets = nil
}

// estimateInputTokens estimates the input tokens of req with tok, or by byte
// length (an upper bound) when tok is nil or the prompt is very large.
func estimateInputTokens(tok Tokenizer, req Request) int64 {
	size := 0
	for _, item := range req.Input {
		size += len(item.Content)
	}
	if tok == nil || size > maxPreciseEstimateBytes {
		return int64(size)
	}
	total := 0
	for _, item := range req.Input {
		total += tok.Count(item.Content)
	}
	return int64(total)
}
