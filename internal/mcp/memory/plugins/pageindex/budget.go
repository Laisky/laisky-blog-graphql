package pageindex

import (
	"math"
	"sync"

	errors "github.com/Laisky/errors/v2"
)

// ErrBudgetExceeded is returned when the shared token budget runs out.
var ErrBudgetExceeded = errors.New("token budget exceeded")

// errRollingBudgetExceeded is the ErrBudgetExceeded refusal of a rolling tenant
// or global budget, whose spend leaves its sliding window, as opposed to an
// operation's own ceiling.
var errRollingBudgetExceeded = errors.Wrap(ErrBudgetExceeded, "rolling llm budget exhausted")

// unlimited marks a Budget dimension that has no ceiling.
const unlimited = -1

// Budget is the §2.6.4.2 gate shared by the goroutines of one operation (one
// indexing run or one search). It always tracks tokens; an operation budget also
// caps upstream calls and tree nodes and names the tenant whose rolling budget
// the operation draws from.
type Budget struct {
	mu        sync.Mutex
	remaining int64
	calls     int64
	nodes     int64
	tenant    string
	// stats, when set, records every upstream request settled against the budget.
	stats *Stats
}

// NewBudget seeds a token-only gate with the supplied initial token count.
func NewBudget(initial int64) *Budget {
	return &Budget{remaining: initial, calls: unlimited, nodes: unlimited}
}

// newOperationBudget seeds the gate of one operation with absolute ceilings.
func newOperationBudget(tokens int64, calls, nodes int, tenant string) *Budget {
	return &Budget{remaining: tokens, calls: int64(calls), nodes: int64(nodes), tenant: tenant}
}

// metered makes b record every upstream request it settles in stats and returns b.
func (b *Budget) metered(stats *Stats) *Budget {
	if b != nil {
		b.stats = stats
	}
	return b
}

// recordUpstream records one upstream request settled against b at charged tokens.
func (b *Budget) recordUpstream(charged int64, reported, retry bool) {
	if b != nil {
		b.stats.addUpstream(charged, reported, retry)
	}
}

// satAdd returns a+b, saturated at the int64 bounds, so a reported charge, however
// absurd, can only exhaust a budget and never wrap it into credit.
func satAdd(a, b int64) int64 {
	switch {
	case b > 0 && a > math.MaxInt64-b:
		return math.MaxInt64
	case b < 0 && a < math.MinInt64-b:
		return math.MinInt64
	}
	return a + b
}

// Take deducts n and reports whether budget remained at or above zero AFTER the deduction.
func (b *Budget) Take(n int64) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining -= n
	return b.remaining >= 0
}

// Remaining returns the current budget. May be negative if Take overshot.
func (b *Budget) Remaining() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining
}

// Tenant returns the tenant whose rolling budget the operation draws from.
func (b *Budget) Tenant() string {
	if b == nil {
		return ""
	}
	return b.tenant
}

// exhausted reports, without reserving anything, that no further call can be admitted.
func (b *Budget) exhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.remaining <= 0 || b.calls == 0
}

// reserveUpTo claims one call, input tokens and an output allowance of at most
// maxOut tokens, the most that fits the remaining tokens, so the token ceiling
// holds before a call is sent rather than after it is billed. It refuses,
// claiming nothing, when no call is left or less than minOut tokens of output
// would fit, and returns the granted output allowance otherwise. The caller
// releases or settles input plus that allowance.
func (b *Budget) reserveUpTo(input, maxOut, minOut int64) (int64, bool) {
	if b == nil {
		return maxOut, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls == 0 || b.remaining <= 0 {
		return 0, false
	}
	out := min(maxOut, b.remaining-input)
	if out < minOut {
		return 0, false
	}
	b.remaining -= input + out
	if b.calls > 0 {
		b.calls--
	}
	return out, true
}

// release returns a reservation whose call was never sent.
func (b *Budget) release(estimate int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining = satAdd(b.remaining, estimate)
	if b.calls >= 0 {
		b.calls++
	}
}

// settle replaces a reservation's estimate with the tokens actually charged.
func (b *Budget) settle(estimate, charged int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remaining = satAdd(b.remaining, satAdd(estimate, -charged))
}

// takeNodes claims n tree nodes and reports whether they fit the node ceiling.
// A refusal claims nothing.
func (b *Budget) takeNodes(n int) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.nodes == unlimited {
		return true
	}
	if int64(n) > b.nodes {
		return false
	}
	b.nodes -= int64(n)
	return true
}

// nodesLeft returns how many more nodes fit, or -1 when nodes are unlimited.
func (b *Budget) nodesLeft() int {
	if b == nil {
		return unlimited
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.nodes)
}
