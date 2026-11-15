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
// Each budget is a strict sliding window: in any interval of the configured
// window's length it admits at most its call and token ceilings. A budget is a
// ledger of what it admitted in each of the last spendSlots+1 slots of
// window/spendSlots, and a call is admitted only when everything recorded in
// those slots plus the call's reservation fits; every interval of the window's
// length lies within spendSlots+1 consecutive slots, so the check covers it. A
// reservation therefore counts for between one window and one window plus one
// slot after its admission. This is stricter than a token bucket of the same
// capacity, which refills while it is spent and so admits up to twice its
// capacity within one window (docs/ref/pageindex_structural_budgets_20261108.md).
// Ledgers are read lazily, so the governor runs no goroutine. A tenant ledger
// that holds nothing and has no call in flight is equivalent to an absent one and
// is dropped, which bounds retained state by recent spenders rather than by every
// tenant ever seen; its slots are bounded by the calls the global budget admits.
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
	tenantCalls, tenantTokens   int64
	globalCalls, globalTokens   int64
	reserveCalls, reserveTokens int64
	lightCalls, lightTokens     int64
	window                      time.Duration
	// slot is the ledger's slot length and span the number of slots per window.
	slot       time.Duration
	span       int64
	epoch      time.Time
	maxTenants int
	now        func() time.Time
	log        logSDK.Logger

	mu sync.Mutex
	// newest is the latest slot seen, so a clock that steps backwards never
	// records into, or reopens, an older slot.
	newest    int64
	global    spendLedger
	tenants   map[string]*spendLedger
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

// spendSlots is the number of slots one window is divided into. A reservation is
// counted for at most one slot (1/spendSlots of the window) longer than the
// window, so the ceiling is conservative by at most that much.
const spendSlots = 60

// spendSlot is what one budget admitted during one slot.
type spendSlot struct {
	index         int64
	calls, tokens int64
}

// spendLedger is one budget: the calls and tokens it admitted in each slot that
// is still counted, oldest first, and their totals.
type spendLedger struct {
	slots         []spendSlot
	calls, tokens int64
	// inflight counts admitted calls not yet settled; such a ledger is never
	// dropped, so a settle always corrects the ledger that admitted it.
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
	slot := max(limits.Window/spendSlots, time.Nanosecond)
	start := now()
	g := &SpendGovernor{
		tenantCalls:   limits.TenantLLMCallsPerWindow,
		tenantTokens:  limits.TenantTokensPerWindow,
		globalCalls:   limits.GlobalLLMCallsPerWindow,
		globalTokens:  limits.GlobalTokensPerWindow,
		reserveCalls:  percentOf(limits.GlobalLLMCallsPerWindow, limits.GlobalReservePercent),
		reserveTokens: percentOf(limits.GlobalTokensPerWindow, limits.GlobalReservePercent),
		lightCalls:    min(int64(limits.MaxLLMCallsPerDocument), limits.TenantLLMCallsPerWindow),
		lightTokens:   min(limits.MaxTokensPerDocument, limits.TenantTokensPerWindow),
		window:        limits.Window,
		slot:          slot,
		span:          int64((limits.Window + slot - 1) / slot),
		epoch:         start,
		maxTenants:    limits.MaxTrackedTenants,
		now:           now,
		log:           log,
		tenants:       make(map[string]*spendLedger),
	}
	g.lastSweep = start
	g.lastLog = start
	return g
}

// spendTicket is one admitted call whose token estimate is settled afterwards.
type spendTicket struct {
	governor *SpendGovernor
	tenant   *spendLedger
	// slot is the slot the call was admitted, and its estimate recorded, in.
	slot     int64
	estimate int64
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
	reason := g.checkLocked(tenant, 0, g.slotLocked())
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
	return g.checkLocked(tenant, 0, g.slotLocked()) == refusalNone
}

// admit spends one call and estimate tokens from the tenant's budget and the
// global budget in one step. A refusal spends nothing.
func (g *SpendGovernor) admit(tenant string, estimate int64) (*spendTicket, bool) {
	if g == nil {
		return nil, true
	}
	est := max(estimate, 0)
	g.mu.Lock()
	slot := g.slotLocked()
	reason := g.checkLocked(tenant, est, slot)
	g.countLocked(reason)
	var ticket *spendTicket
	if reason == refusalNone {
		budget := g.tenants[tenant]
		if budget == nil {
			budget = &spendLedger{}
			g.tenants[tenant] = budget
		}
		budget.record(slot, 1, est)
		budget.inflight++
		g.global.record(slot, 1, est)
		g.stats.Admitted++
		ticket = &spendTicket{governor: g, tenant: budget, slot: slot, estimate: est}
	}
	summary, emit := g.summaryLocked()
	g.mu.Unlock()
	g.emit(summary, emit)
	return ticket, ticket != nil
}

// slotLocked returns the current slot, never one older than a slot already
// seen. The caller holds g.mu.
func (g *SpendGovernor) slotLocked() int64 {
	if elapsed := g.now().Sub(g.epoch); elapsed > 0 {
		g.newest = max(g.newest, int64(elapsed/g.slot))
	}
	return g.newest
}

// oldestCounted returns the oldest slot still counted at slot: spendSlots+1
// slots cover every interval of one window that ends in slot.
func (g *SpendGovernor) oldestCounted(slot int64) int64 { return slot - g.span }

// checkLocked decides whether tenant may spend one call and est tokens in slot.
// It expires the slots it reads and may sweep, but spends nothing. The caller
// holds g.mu.
func (g *SpendGovernor) checkLocked(tenant string, est, slot int64) refusal {
	now := g.now()
	oldest := g.oldestCounted(slot)
	g.sweepLocked(now, oldest, g.window/sweepIdleDivisor)

	budget := g.tenants[tenant]
	var usedCalls, usedTokens int64
	if budget != nil {
		budget.expire(oldest)
		if !budget.fits(g.tenantCalls, g.tenantTokens, est) {
			return refusalTenant
		}
		usedCalls, usedTokens = budget.calls, budget.tokens
	} else if est > g.tenantTokens {
		return refusalTenant
	}
	g.global.expire(oldest)
	if !g.global.fits(g.globalCalls, g.globalTokens, est) {
		return refusalGlobal
	}
	inReserve := g.globalCalls-g.global.calls-1 < g.reserveCalls || g.globalTokens-g.global.tokens-est < g.reserveTokens
	if inReserve && (usedCalls >= g.lightCalls || usedTokens >= g.lightTokens) {
		return refusalReserve
	}
	if budget == nil && len(g.tenants) >= g.maxTenants && !g.sweepLocked(now, oldest, g.window/sweepForcedDivisor) {
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
// A refund is returned to the admission slot, which held the estimate, so it
// frees exactly what the reservation took and is dropped once that slot is no
// longer counted. A charge above the estimate is debt, recorded in the current
// slot: it counts for a whole window from when it became known, so the time a
// slow call spent in flight cannot shorten it, and a budget in debt admits
// nothing. The admitting ledger is still tracked because ledgers with calls in
// flight are never swept. Settling twice is a no-op.
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
	slot := g.slotLocked()
	oldest := g.oldestCounted(slot)
	delta := max(charged, 0) - t.estimate
	for _, ledger := range []*spendLedger{t.tenant, &g.global} {
		ledger.expire(oldest)
		switch {
		case delta < 0:
			ledger.refund(t.slot, -delta)
		case delta > 0:
			ledger.record(slot, 0, delta)
		}
	}
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

// sweepLocked drops every tenant ledger that holds nothing still counted at
// oldest and has no call in flight, at most once per interval, and reports
// whether the table has room for another tenant. A dropped ledger was empty, so
// dropping it changes no decision. The caller holds g.mu.
func (g *SpendGovernor) sweepLocked(now time.Time, oldest int64, interval time.Duration) bool {
	if now.Sub(g.lastSweep) < interval {
		return len(g.tenants) < g.maxTenants
	}
	g.lastSweep = now
	for name, budget := range g.tenants {
		budget.expire(oldest)
		if budget.inflight == 0 && len(budget.slots) == 0 {
			delete(g.tenants, name)
		}
	}
	return len(g.tenants) < g.maxTenants
}

// expire drops the slots older than oldest from l's totals.
func (l *spendLedger) expire(oldest int64) {
	n := 0
	for n < len(l.slots) && l.slots[n].index < oldest {
		l.calls -= l.slots[n].calls
		l.tokens -= l.slots[n].tokens
		n++
	}
	if n == 0 {
		return
	}
	l.slots = append(l.slots[:0], l.slots[n:]...)
}

// fits reports whether l, under the given ceilings, has room for one more call
// and estimate tokens, and has not spent its whole token ceiling (so a ledger in
// debt admits nothing).
func (l *spendLedger) fits(callCap, tokenCap, estimate int64) bool {
	return l.calls+1 <= callCap && l.tokens < tokenCap && l.tokens+estimate <= tokenCap
}

// record adds calls and tokens to slot, which is never older than l's newest slot.
func (l *spendLedger) record(slot, calls, tokens int64) {
	if last := len(l.slots) - 1; last >= 0 && l.slots[last].index >= slot {
		l.slots[last].calls += calls
		l.slots[last].tokens += tokens
	} else {
		l.slots = append(l.slots, spendSlot{index: slot, calls: calls, tokens: tokens})
	}
	l.calls += calls
	l.tokens += tokens
}

// refund returns up to tokens to slot when that slot is still counted.
func (l *spendLedger) refund(slot, tokens int64) {
	for i := len(l.slots) - 1; i >= 0 && l.slots[i].index >= slot; i-- {
		if l.slots[i].index == slot {
			give := min(tokens, l.slots[i].tokens)
			l.slots[i].tokens -= give
			l.tokens -= give
			return
		}
	}
}

// percentOf returns pct percent of v, rounded down, without overflowing for any
// positive v.
func percentOf(v int64, pct int) int64 {
	p := int64(pct)
	return v/100*p + v%100*p/100
}
