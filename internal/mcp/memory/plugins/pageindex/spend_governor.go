package pageindex

import (
	"sync"
	"time"
)

// SpendGovernor is the rolling, process-wide budget on upstream LLM spend. It keeps
// one budget per tenant, so repeated writes or searches by one caller cannot
// amplify without bound, and one global budget shared by every tenant, which is
// the operator's ceiling even when callers mint new tenant identities.
//
// Each budget is a pair of token buckets (calls and tokens) that refill linearly
// over the configured window and are read lazily, so the governor runs no
// goroutine. A tenant budget that has refilled completely is equivalent to an
// absent one and is dropped, which bounds retained state by recent spenders
// rather than by every tenant ever seen; MaxTrackedTenants is a hard cap on top.
// A tenant that cannot be tracked is refused (its work degrades to local
// summaries) rather than given an untracked budget.
type SpendGovernor struct {
	tenantCalls, tenantTokens float64
	globalCalls, globalTokens float64
	window                    time.Duration
	maxTenants                int
	now                       func() time.Time

	mu        sync.Mutex
	global    spendBuckets
	tenants   map[string]*spendBuckets
	lastSweep time.Time
	stats     SpendStats
}

// SpendStats is a snapshot of a SpendGovernor's counters.
type SpendStats struct {
	// Admitted counts admitted upstream calls.
	Admitted uint64
	// TenantRefusals counts calls refused by a tenant budget.
	TenantRefusals uint64
	// GlobalRefusals counts calls refused by the global budget.
	GlobalRefusals uint64
	// UntrackedRefusals counts calls refused because MaxTrackedTenants was reached.
	UntrackedRefusals uint64
	// TrackedTenants is the number of tenant budgets currently held.
	TrackedTenants int
}

// spendBuckets is one budget: a call bucket and a token bucket refilled together.
type spendBuckets struct {
	calls, tokens float64
	last          time.Time
}

// NewSpendGovernor builds a governor from limits; unset limits take their defaults.
// Every budget starts full.
func NewSpendGovernor(limits LimitsSettings) *SpendGovernor {
	limits = limits.WithDefaults()
	g := &SpendGovernor{
		tenantCalls:  float64(limits.TenantLLMCallsPerWindow),
		tenantTokens: float64(limits.TenantTokensPerWindow),
		globalCalls:  float64(limits.GlobalLLMCallsPerWindow),
		globalTokens: float64(limits.GlobalTokensPerWindow),
		window:       limits.Window,
		maxTenants:   limits.MaxTrackedTenants,
		now:          time.Now,
		tenants:      make(map[string]*spendBuckets),
	}
	now := g.now()
	g.global = spendBuckets{calls: g.globalCalls, tokens: g.globalTokens, last: now}
	g.lastSweep = now
	return g
}

// spendTicket is one admitted call whose token estimate is settled afterwards.
type spendTicket struct {
	governor *SpendGovernor
	tenant   *spendBuckets
	estimate float64
}

// wouldRefuse reports, without spending, that tenant could not be admitted for
// even the smallest call. Callers use it to skip estimating a prompt.
func (g *SpendGovernor) wouldRefuse(tenant string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.global.refill(now, g.globalCalls, g.globalTokens, g.window)
	if !g.global.canSpend(0) {
		return true
	}
	budget := g.tenants[tenant]
	if budget == nil {
		return len(g.tenants) >= g.maxTenants && !g.sweepLocked(now, g.window/sweepForcedDivisor)
	}
	budget.refill(now, g.tenantCalls, g.tenantTokens, g.window)
	return !budget.canSpend(0)
}

// admit spends one call and estimate tokens from the tenant's budget and the
// global budget in one step. A refusal spends nothing.
func (g *SpendGovernor) admit(tenant string, estimate int64) (*spendTicket, bool) {
	if g == nil {
		return nil, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.sweepLocked(now, g.window/sweepIdleDivisor)

	est := float64(max(estimate, 0))
	budget := g.tenants[tenant]
	if budget != nil {
		budget.refill(now, g.tenantCalls, g.tenantTokens, g.window)
		if !budget.canSpend(est) {
			g.stats.TenantRefusals++
			return nil, false
		}
	} else if est > g.tenantTokens {
		g.stats.TenantRefusals++
		return nil, false
	}
	g.global.refill(now, g.globalCalls, g.globalTokens, g.window)
	if !g.global.canSpend(est) {
		g.stats.GlobalRefusals++
		return nil, false
	}
	if budget == nil {
		if len(g.tenants) >= g.maxTenants && !g.sweepLocked(now, g.window/sweepForcedDivisor) {
			g.stats.UntrackedRefusals++
			return nil, false
		}
		budget = &spendBuckets{calls: g.tenantCalls, tokens: g.tenantTokens, last: now}
		g.tenants[tenant] = budget
	}
	budget.spend(est)
	g.global.spend(est)
	g.stats.Admitted++
	return &spendTicket{governor: g, tenant: budget, estimate: est}, true
}

// settle replaces the ticket's token estimate with the tokens actually charged.
// A charge above the estimate leaves the budgets in debt until they refill.
func (t *spendTicket) settle(charged int64) {
	if t == nil {
		return
	}
	g := t.governor
	g.mu.Lock()
	defer g.mu.Unlock()
	delta := float64(max(charged, 0)) - t.estimate
	t.tenant.tokens = min(g.tenantTokens, t.tenant.tokens-delta)
	g.global.tokens = min(g.globalTokens, g.global.tokens-delta)
}

// Stats returns a snapshot of the governor's counters.
func (g *SpendGovernor) Stats() SpendStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	stats := g.stats
	stats.TrackedTenants = len(g.tenants)
	return stats
}

// Sweep cadences, as fractions of the window. A routine sweep runs at most once
// per window/sweepIdleDivisor; when the tenant table is full, a sweep that could
// make room runs at most once per window/sweepForcedDivisor, so a flood of new
// tenants cannot turn every admission into a full scan.
const (
	sweepIdleDivisor   = 100
	sweepForcedDivisor = 10_000
)

// sweepLocked drops every tenant budget that has refilled completely, at most once
// per interval, and reports whether the table has room for another tenant. A
// dropped budget was full, so dropping it changes no decision. The caller holds g.mu.
func (g *SpendGovernor) sweepLocked(now time.Time, interval time.Duration) bool {
	if now.Sub(g.lastSweep) < interval {
		return len(g.tenants) < g.maxTenants
	}
	g.lastSweep = now
	for name, budget := range g.tenants {
		budget.refill(now, g.tenantCalls, g.tenantTokens, g.window)
		if budget.full(g.tenantCalls, g.tenantTokens) {
			delete(g.tenants, name)
		}
	}
	return len(g.tenants) < g.maxTenants
}

// refill credits b with the calls and tokens earned since it was last read, up to
// its capacity. A clock that steps backwards earns nothing.
func (b *spendBuckets) refill(now time.Time, callCap, tokenCap float64, window time.Duration) {
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.last = now
	share := float64(elapsed) / float64(window)
	b.calls = min(callCap, b.calls+share*callCap)
	b.tokens = min(tokenCap, b.tokens+share*tokenCap)
}

// canSpend reports whether b holds a whole call and at least estimate tokens
// (and some tokens at all, so a budget in debt admits nothing).
func (b *spendBuckets) canSpend(estimate float64) bool {
	const epsilon = 1e-6
	return b.calls+epsilon >= 1 && b.tokens > 0 && b.tokens+epsilon >= estimate
}

// spend takes one call and estimate tokens from b.
func (b *spendBuckets) spend(estimate float64) {
	b.calls = max(0, b.calls-1)
	b.tokens -= estimate
}

// full reports whether b has refilled to its capacity.
func (b *spendBuckets) full(callCap, tokenCap float64) bool {
	const epsilon = 1e-6
	return b.calls+epsilon >= callCap && b.tokens+epsilon >= tokenCap
}
