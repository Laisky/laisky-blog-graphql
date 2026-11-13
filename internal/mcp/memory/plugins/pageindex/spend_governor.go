package pageindex

import (
	"sync"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	glog "github.com/Laisky/zap"
)

// SpendGovernor is the rolling, process-wide budget on upstream LLM spend. It keeps
// one budget per tenant, so repeated writes or searches by one caller cannot
// amplify without bound, and one global budget shared by every tenant, which is
// the operator's ceiling even when callers use many tenant identities.
//
// Each budget is a pair of token buckets (calls and tokens) that refill linearly
// over the configured window and are read lazily, so the governor runs no
// goroutine. A tenant budget that has refilled completely and has no call in
// flight is equivalent to an absent one and is dropped, which bounds retained
// state by recent spenders rather than by every tenant ever seen;
// MaxTrackedTenants is a hard cap on top. A tenant that cannot be tracked is
// refused (its work degrades) rather than given an untracked budget.
//
// Fairness: the last GlobalReservePercent of the global budget is reserved for
// light tenants, those that have spent less than one document's allowance
// (MaxLLMCallsPerDocument calls and MaxTokensPerDocument tokens) of their own
// budget in the current window. Heavy tenants are refused once the global budget
// reaches the reserve, so one heavy caller cannot starve another caller's first
// document; each tenant can draw at most one document's allowance from the reserve.
type SpendGovernor struct {
	tenantCalls, tenantTokens   float64
	globalCalls, globalTokens   float64
	reserveCalls, reserveTokens float64
	lightCalls, lightTokens     float64
	window                      time.Duration
	maxTenants                  int
	now                         func() time.Time
	log                         logSDK.Logger

	mu        sync.Mutex
	global    spendBuckets
	tenants   map[string]*spendBuckets
	lastSweep time.Time
	stats     SpendStats
	logged    SpendStats
	lastLog   time.Time
}

// SpendStats is a snapshot of a SpendGovernor's counters. Every refusal is
// counted, including the cheap pre-checks that skip rendering a prompt.
type SpendStats struct {
	// Admitted counts admitted upstream calls.
	Admitted uint64
	// TenantRefusals counts calls refused by a tenant budget.
	TenantRefusals uint64
	// GlobalRefusals counts calls refused because the global budget was empty.
	GlobalRefusals uint64
	// ReserveRefusals counts calls by heavy tenants refused at the global reserve.
	ReserveRefusals uint64
	// UntrackedRefusals counts calls refused because MaxTrackedTenants was reached.
	UntrackedRefusals uint64
	// TrackedTenants is the number of tenant budgets currently held.
	TrackedTenants int
}

// refusals returns the total refusal count.
func (s SpendStats) refusals() uint64 {
	return s.TenantRefusals + s.GlobalRefusals + s.ReserveRefusals + s.UntrackedRefusals
}

// refusalLogInterval is the shortest interval between two refusal summary logs.
const refusalLogInterval = time.Minute

// spendBuckets is one budget: a call bucket and a token bucket refilled together.
type spendBuckets struct {
	calls, tokens float64
	last          time.Time
	// inflight counts admitted calls not yet settled; such a bucket is never
	// dropped, so a settle always charges the bucket that admitted it.
	inflight int
}

// NewSpendGovernor builds a governor from limits on the wall clock; unset limits
// take their defaults. Every budget starts full.
func NewSpendGovernor(limits LimitsSettings) *SpendGovernor {
	return NewSpendGovernorWithClock(limits, time.Now, nil)
}

// NewSpendGovernorWithClock builds a governor that reads time from now and
// reports refusal summaries to log (which may be nil). Tests inject a fake clock
// so that budget decisions never depend on how long a run takes.
func NewSpendGovernorWithClock(limits LimitsSettings, now func() time.Time, log logSDK.Logger) *SpendGovernor {
	limits = limits.WithDefaults()
	if now == nil {
		now = time.Now
	}
	reserve := float64(limits.GlobalReservePercent) / 100
	g := &SpendGovernor{
		tenantCalls:   float64(limits.TenantLLMCallsPerWindow),
		tenantTokens:  float64(limits.TenantTokensPerWindow),
		globalCalls:   float64(limits.GlobalLLMCallsPerWindow),
		globalTokens:  float64(limits.GlobalTokensPerWindow),
		reserveCalls:  reserve * float64(limits.GlobalLLMCallsPerWindow),
		reserveTokens: reserve * float64(limits.GlobalTokensPerWindow),
		lightCalls:    min(float64(limits.MaxLLMCallsPerDocument), float64(limits.TenantLLMCallsPerWindow)),
		lightTokens:   min(float64(limits.MaxTokensPerDocument), float64(limits.TenantTokensPerWindow)),
		window:        limits.Window,
		maxTenants:    limits.MaxTrackedTenants,
		now:           now,
		log:           log,
		tenants:       make(map[string]*spendBuckets),
	}
	start := g.now()
	g.global = spendBuckets{calls: g.globalCalls, tokens: g.globalTokens, last: start}
	g.lastSweep = start
	g.lastLog = start
	return g
}

// spendTicket is one admitted call whose token estimate is settled afterwards.
type spendTicket struct {
	governor *SpendGovernor
	tenant   *spendBuckets
	estimate float64
	settled  bool
}

// refusal names the budget that refused a call.
type refusal int

const (
	refusalNone refusal = iota
	refusalTenant
	refusalGlobal
	refusalReserve
	refusalUntracked
)

// wouldRefuse reports, without spending, that tenant could not be admitted for
// even the smallest call. Callers use it to skip rendering a prompt; the refusal
// is counted like any other.
func (g *SpendGovernor) wouldRefuse(tenant string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	reason := g.checkLocked(tenant, 0)
	g.countLocked(reason)
	summary, emit := g.summaryLocked()
	g.mu.Unlock()
	g.emit(summary, emit)
	return reason != refusalNone
}

// hasBudget reports, without spending or counting a refusal, whether tenant could
// be admitted for one call now. Background work uses it to wait for budget.
func (g *SpendGovernor) hasBudget(tenant string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkLocked(tenant, 0) == refusalNone
}

// admit spends one call and estimate tokens from the tenant's budget and the
// global budget in one step. A refusal spends nothing.
func (g *SpendGovernor) admit(tenant string, estimate int64) (*spendTicket, bool) {
	if g == nil {
		return nil, true
	}
	est := float64(max(estimate, 0))
	g.mu.Lock()
	reason := g.checkLocked(tenant, est)
	g.countLocked(reason)
	var ticket *spendTicket
	if reason == refusalNone {
		budget := g.tenants[tenant]
		if budget == nil {
			budget = &spendBuckets{calls: g.tenantCalls, tokens: g.tenantTokens, last: g.now()}
			g.tenants[tenant] = budget
		}
		budget.spend(est)
		budget.inflight++
		g.global.spend(est)
		g.stats.Admitted++
		ticket = &spendTicket{governor: g, tenant: budget, estimate: est}
	}
	summary, emit := g.summaryLocked()
	g.mu.Unlock()
	g.emit(summary, emit)
	return ticket, ticket != nil
}

// checkLocked decides whether tenant may spend one call and est tokens now. It
// refills the buckets it reads and may sweep, but spends nothing. The caller
// holds g.mu.
func (g *SpendGovernor) checkLocked(tenant string, est float64) refusal {
	now := g.now()
	g.sweepLocked(now, g.window/sweepIdleDivisor)

	budget := g.tenants[tenant]
	usedCalls, usedTokens := 0.0, 0.0
	if budget != nil {
		budget.refill(now, g.tenantCalls, g.tenantTokens, g.window)
		if !budget.canSpend(est) {
			return refusalTenant
		}
		usedCalls, usedTokens = g.tenantCalls-budget.calls, g.tenantTokens-budget.tokens
	} else if est > g.tenantTokens {
		return refusalTenant
	}
	g.global.refill(now, g.globalCalls, g.globalTokens, g.window)
	if !g.global.canSpend(est) {
		return refusalGlobal
	}
	inReserve := g.global.calls-1 < g.reserveCalls || g.global.tokens-est < g.reserveTokens
	if inReserve && (usedCalls >= g.lightCalls || usedTokens >= g.lightTokens) {
		return refusalReserve
	}
	if budget == nil && len(g.tenants) >= g.maxTenants && !g.sweepLocked(now, g.window/sweepForcedDivisor) {
		return refusalUntracked
	}
	return refusalNone
}

// countLocked records a refusal. The caller holds g.mu.
func (g *SpendGovernor) countLocked(reason refusal) {
	switch reason {
	case refusalNone:
	case refusalTenant:
		g.stats.TenantRefusals++
	case refusalGlobal:
		g.stats.GlobalRefusals++
	case refusalReserve:
		g.stats.ReserveRefusals++
	case refusalUntracked:
		g.stats.UntrackedRefusals++
	}
}

// summaryLocked returns the refusal counters since the last summary log when a
// new summary is due: at most once per refusalLogInterval and only when calls
// were refused. The caller holds g.mu.
func (g *SpendGovernor) summaryLocked() (SpendStats, bool) {
	if g.log == nil || g.stats.refusals() == g.logged.refusals() {
		return SpendStats{}, false
	}
	now := g.now()
	if now.Sub(g.lastLog) < refusalLogInterval {
		return SpendStats{}, false
	}
	delta := SpendStats{
		Admitted:          g.stats.Admitted - g.logged.Admitted,
		TenantRefusals:    g.stats.TenantRefusals - g.logged.TenantRefusals,
		GlobalRefusals:    g.stats.GlobalRefusals - g.logged.GlobalRefusals,
		ReserveRefusals:   g.stats.ReserveRefusals - g.logged.ReserveRefusals,
		UntrackedRefusals: g.stats.UntrackedRefusals - g.logged.UntrackedRefusals,
		TrackedTenants:    len(g.tenants),
	}
	g.logged = g.stats
	g.lastLog = now
	return delta, true
}

// emit logs one refusal summary outside the governor lock.
func (g *SpendGovernor) emit(delta SpendStats, ok bool) {
	if !ok {
		return
	}
	g.log.Warn("pageindex.spend.refusals",
		glog.Uint64("admitted", delta.Admitted),
		glog.Uint64("tenant_refusals", delta.TenantRefusals),
		glog.Uint64("global_refusals", delta.GlobalRefusals),
		glog.Uint64("reserve_refusals", delta.ReserveRefusals),
		glog.Uint64("untracked_refusals", delta.UntrackedRefusals),
		glog.Int("tracked_tenants", delta.TrackedTenants),
	)
}

// settle replaces the ticket's token estimate with the tokens actually charged.
// A charge above the estimate leaves the budgets in debt until they refill. The
// admitting bucket is still tracked because buckets with calls in flight are
// never swept. Settling twice is a no-op.
func (t *spendTicket) settle(charged int64) {
	if t == nil {
		return
	}
	g := t.governor
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.settled {
		return
	}
	t.settled = true
	t.tenant.inflight--
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

// sweepLocked drops every tenant budget that has refilled completely and has no
// call in flight, at most once per interval, and reports whether the table has
// room for another tenant. A dropped budget was full, so dropping it changes no
// decision. The caller holds g.mu.
func (g *SpendGovernor) sweepLocked(now time.Time, interval time.Duration) bool {
	if now.Sub(g.lastSweep) < interval {
		return len(g.tenants) < g.maxTenants
	}
	g.lastSweep = now
	for name, budget := range g.tenants {
		budget.refill(now, g.tenantCalls, g.tenantTokens, g.window)
		if budget.inflight == 0 && budget.full(g.tenantCalls, g.tenantTokens) {
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
