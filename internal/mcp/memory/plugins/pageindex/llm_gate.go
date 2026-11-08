package pageindex

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

// llmAdmission is one admitted call, settled exactly once after it returns.
type llmAdmission struct {
	budget   *Budget
	ticket   *spendTicket
	estimate int64
}

// wouldRefuse reports, cheaply and without spending, that no call can be admitted
// for budget, so callers can skip rendering and estimating a prompt.
func (g llmGate) wouldRefuse(budget *Budget) bool {
	return budget.exhausted() || g.spend.wouldRefuse(budget.Tenant())
}

// admit reserves req's estimated input tokens and one call from budget and from the
// rolling budgets. It returns ErrBudgetExceeded, reserving nothing, when any of
// them would be exceeded.
func (g llmGate) admit(req Request, budget *Budget) (*llmAdmission, error) {
	if g.wouldRefuse(budget) {
		return nil, ErrBudgetExceeded
	}
	estimate := estimateInputTokens(g.tok, req)
	if !budget.reserve(estimate) {
		return nil, ErrBudgetExceeded
	}
	ticket, ok := g.spend.admit(budget.Tenant(), estimate)
	if !ok {
		budget.release(estimate)
		return nil, ErrBudgetExceeded
	}
	return &llmAdmission{budget: budget, ticket: ticket, estimate: estimate}, nil
}

// settle charges the call: the provider-reported total when it reports one,
// otherwise the input estimate. A failed call is charged its estimate, since the
// provider may have billed it.
func (a *llmAdmission) settle(resp *Response) {
	charged := a.estimate
	if resp != nil && resp.Usage.TotalTokens > 0 {
		charged = int64(resp.Usage.TotalTokens)
	}
	a.budget.settle(a.estimate, charged)
	a.ticket.settle(charged)
}

// release returns an admission whose call was never sent: the operation budget is
// restored and the rolling budgets keep only the call.
func (a *llmAdmission) release() {
	a.budget.release(a.estimate)
	a.ticket.settle(0)
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
