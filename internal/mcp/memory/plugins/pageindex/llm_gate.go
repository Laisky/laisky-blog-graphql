package pageindex

import (
	"context"
	"sync"

	errors "github.com/Laisky/errors/v2"
	openai "github.com/openai/openai-go"
)

// maxPreciseEstimateBytes is the largest prompt that is tokenized to estimate its
// cost. A larger prompt is estimated by its byte length, which bounds its token
// count from above, so estimating never costs more than tokenizing this much.
const maxPreciseEstimateBytes = 256 << 10

// inputOverheadTokens is added to the estimate of every input message for the
// role and framing tokens the provider counts but the prompt text does not hold.
const inputOverheadTokens = 8

// errOutputCeiling is the ErrBudgetExceeded refusal of a response the provider
// cut at its output ceiling: its text is incomplete, so it is neither used nor
// cached, and the work it was for degrades like any other refused call.
var errOutputCeiling = errors.Wrap(ErrBudgetExceeded, "llm output reached its token ceiling")

// errIncompleteAnswer is the ErrBudgetExceeded refusal of an answer the provider
// did not complete for another reason (a content filter, a failure, or no reason
// given): like a truncated answer it is charged but neither used nor cached, and
// the work it was for degrades instead of failing the run.
var errIncompleteAnswer = errors.Wrap(ErrBudgetExceeded, "llm answer incomplete")

// llmGate is the single admission point every upstream LLM call passes through,
// for indexing and for search. Before a request is sent it reserves the input
// estimate plus an output allowance, sent as max_output_tokens, from the
// operation's absolute ceilings and from the rolling tenant and global budgets
// of the SpendGovernor, so a call's whole cost is paid for before it is incurred.
type llmGate struct {
	tok   Tokenizer
	spend *SpendGovernor
	// maxOutput and maxLongOutput are the output ceilings of one request with a
	// short answer and of one that re-emits a table of contents (limits.*).
	maxOutput, maxLongOutput int64
}

// newLLMGate builds the gate of one plugin from its limits.
func newLLMGate(tok Tokenizer, spend *SpendGovernor, limits LimitsSettings) llmGate {
	limits = limits.WithDefaults()
	return llmGate{tok: tok, spend: spend, maxOutput: int64(limits.MaxOutputTokensPerCall), maxLongOutput: int64(limits.MaxTOCOutputTokensPerCall)}
}

// llmAdmission is one admitted call and every retry of it admitted since. Each
// upstream request holds its own reservation of input plus output tokens, which
// is settled exactly once: when a retry follows it, or when the call returns. So
// at most one request is pending at a time.
type llmAdmission struct {
	gate          llmGate
	budget        *Budget
	input, output int64
	mu            sync.Mutex
	// pending is the rolling-budget ticket of the request not yet settled, if any.
	pending *spendTicket
	// sent counts the call's requests that left the process.
	sent int
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

// outputCeiling returns the largest output allowance req may be granted: the
// ceiling of its answer kind, lowered to req.MaxOutTokens when that is set.
func (g llmGate) outputCeiling(req Request) int64 {
	ceiling := g.maxOutput
	if req.LongOutput {
		ceiling = g.maxLongOutput
	}
	if ceiling <= 0 {
		ceiling = DefaultMaxOutputTokensPerCall
	}
	if req.MaxOutTokens > 0 {
		ceiling = min(ceiling, int64(req.MaxOutTokens))
	}
	return ceiling
}

// answerReserve returns the output allowance a short-answer prompt built to fit
// remaining tokens should leave for its answer: a quarter of them, at least
// minOutputTokens and at most the short-answer ceiling.
func (g llmGate) answerReserve(remaining int64) int64 {
	return min(g.outputCeiling(Request{}), max(minOutputTokens, remaining/4))
}

// call sends req to llm through the gate: it reserves the call, binds the granted
// output ceiling and the admission of every retry to the request, and settles
// every upstream request it made. A response the provider cut at the output
// ceiling is returned together with errOutputCeiling, and one it did not
// complete for another reason with errIncompleteAnswer, so the caller can count
// the billed call but must neither use nor cache it.
func (g llmGate) call(ctx context.Context, llm LLM, req Request, budget *Budget) (*Response, error) {
	if err := ctx.Err(); err != nil {
		// Nothing is reserved for a call whose caller has already gone.
		return nil, errors.WithStack(err)
	}
	admission, err := g.admit(req, budget)
	if err != nil {
		return nil, err
	}
	resp, err := llm.Respond(ctx, admission.bind(req))
	admission.settle(resp, err)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.Truncated:
		return resp, errOutputCeiling
	case resp.Incomplete:
		return resp, errIncompleteAnswer
	}
	return resp, nil
}

// admit reserves one call, req's estimated input tokens and the largest output
// allowance up to the request's ceiling that fits the operation's remaining
// tokens, then takes that whole reservation from the rolling budgets. It returns
// ErrBudgetExceeded, reserving nothing, when the operation cannot grant at least
// minOutputTokens of output (or the request's smaller ceiling), and
// errRollingBudgetExceeded, which wraps it, when a rolling budget cannot cover
// the reservation.
func (g llmGate) admit(req Request, budget *Budget) (*llmAdmission, error) {
	if err := g.precheck(budget); err != nil {
		return nil, err
	}
	input := estimateInputTokens(g.tok, req)
	ceiling := g.outputCeiling(req)
	output, ok := budget.reserveUpTo(input, ceiling, min(minOutputTokens, ceiling))
	if !ok {
		return nil, ErrBudgetExceeded
	}
	a := &llmAdmission{gate: g, budget: budget, input: input, output: output}
	if err := a.admitRolling(); err != nil {
		return nil, err
	}
	return a, nil
}

// reservation is what one upstream request of the call may cost at most.
func (a *llmAdmission) reservation() int64 { return a.input + a.output }

// admitOne reserves one more upstream request (a retry) at the call's reservation.
func (a *llmAdmission) admitOne() error {
	if _, ok := a.budget.reserveUpTo(a.input, a.output, a.output); !ok {
		return ErrBudgetExceeded
	}
	return a.admitRolling()
}

// admitRolling takes the request's reservation from the rolling budgets, giving
// the operation's reservation back when they refuse.
func (a *llmAdmission) admitRolling() error {
	ticket, ok := a.gate.spend.admit(a.budget.Tenant(), a.reservation())
	if !ok {
		a.budget.release(a.reservation())
		return errRollingBudgetExceeded
	}
	a.mu.Lock()
	a.pending = ticket
	a.mu.Unlock()
	return nil
}

// bind returns req with the granted output ceiling and with AdmitRetry set, so
// every retry the client makes is admitted against the same budgets as a new
// call before it is sent. The cache key (PromptHash) is computed before bind, so
// the granted ceiling, which depends on the remaining budget, never changes it.
func (a *llmAdmission) bind(req Request) Request {
	req.MaxOutTokens = int(a.output)
	req.AdmitRetry = func(_ int, prev error) error {
		// The failed request is charged before the retry is reserved, so its
		// unused reservation is available to the retry.
		a.settle(nil, prev)
		return a.admitOne()
	}
	return req
}

// settle charges the pending request, if any, with its outcome (resp, err); see
// attemptCharge for what a request costs.
func (a *llmAdmission) settle(resp *Response, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		return
	}
	if errors.Is(err, errRequestNotSent) {
		// Nothing left the process: the call and its tokens are returned.
		a.budget.release(a.reservation())
		a.pending.release()
		a.pending = nil
		return
	}
	charged := a.attemptCharge(resp, err)
	a.budget.settle(a.reservation(), charged)
	a.pending.settle(charged)
	a.pending = nil
	a.sent++
	a.budget.recordUpstream(charged, reportedUsage(resp), a.sent > 1)
}

// reportedUsage reports that resp carries provider-reported usage, which
// attemptCharge charges as reported rather than estimated.
func reportedUsage(resp *Response) bool {
	return resp != nil && resp.Usage.TotalTokens > 0
}

// attemptCharge returns what one upstream request with outcome (resp, err) is
// charged: the provider-reported total when there is one; for a response without
// usage, the input estimate plus the estimated tokens of its text; for an error
// response from the provider, the input estimate, as the provider may have
// counted it; nothing for a request that never left the process; and for a
// request the provider never answered (a timeout, a cancellation or a transport
// failure), its whole reservation, the most it can have cost, since the provider
// may have processed and billed it in full. The call itself stays spent either way.
func (a *llmAdmission) attemptCharge(resp *Response, err error) int64 {
	switch {
	case reportedUsage(resp):
		return min(int64(resp.Usage.TotalTokens), maxRequestCharge)
	case resp != nil:
		return a.input + estimateTextTokens(a.gate.tok, resp.Text)
	case providerAnswered(err):
		return a.input
	case errors.Is(err, errRequestNotSent):
		return 0
	default:
		return a.reservation()
	}
}

// maxRequestCharge caps what one request is charged from reported usage. No real
// request comes near it; it keeps a gateway that reports absurd usage from
// overflowing the budgets' arithmetic, and still exhausts any ceiling.
const maxRequestCharge int64 = 1 << 40

// errDeadlineRefusal is the ErrBudgetExceeded refusal of an indexing request
// that could not be sent before the indexing deadline (errDeadlineAhead): the run
// degrades as if the deadline had passed.
var errDeadlineRefusal = errors.Wrap(ErrBudgetExceeded, "llm request would outlive the indexing deadline")

// providerAnswered reports that err is an error response from the provider, as
// opposed to a request that got no answer.
func providerAnswered(err error) bool {
	var apiErr *openai.Error
	return errors.As(err, &apiErr)
}

// estimateInputTokens estimates the input tokens of req with tok, or by byte
// length (an upper bound) when tok is nil or the prompt is very large, plus the
// framing of every message and the byte length of a structured-output schema.
func estimateInputTokens(tok Tokenizer, req Request) int64 {
	total := int64(len(req.Schema) + inputOverheadTokens*len(req.Input))
	size := 0
	for _, item := range req.Input {
		size += len(item.Content)
	}
	if tok == nil || size > maxPreciseEstimateBytes {
		return total + int64(size)
	}
	for _, item := range req.Input {
		total += int64(tok.Count(item.Content))
	}
	return total
}

// estimateTextTokens estimates the tokens of text with tok, or by byte length
// when tok is nil or the text is very large.
func estimateTextTokens(tok Tokenizer, text string) int64 {
	if tok == nil || len(text) > maxPreciseEstimateBytes {
		return int64(len(text))
	}
	return int64(tok.Count(text))
}
