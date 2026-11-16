package pageindex

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// spendEvent is one admitted call: when it was admitted, by whom and what it was
// finally charged.
type spendEvent struct {
	at      time.Time
	tenant  string
	charged int64
}

// maxInAnyWindow returns the most calls and tokens charged in any half-open
// interval of length window whose events satisfy keep. The maximum is attained by
// an interval that starts at an event, so only those are checked.
func maxInAnyWindow(events []spendEvent, window time.Duration, keep func(spendEvent) bool) (calls, tokens int64) {
	for i, start := range events {
		if !keep(start) {
			continue
		}
		var c, tk int64
		for _, e := range events[i:] {
			if !e.at.Before(start.at.Add(window)) {
				break
			}
			if keep(e) {
				c++
				tk += e.charged
			}
		}
		calls, tokens = max(calls, c), max(tokens, tk)
	}
	return calls, tokens
}

// TestRollingBudgetIsAStrictCeilingOverEveryWindow reproduces the P2 finding of
// the third review of #96: the rolling budgets were token buckets that refill
// continuously, so a full bucket spent at once was nearly full again one window
// later and about twice the configured amount could be admitted within one
// window, although the settings and the manual describe a ceiling per window.
// Callers admit as much as they can at random times, with refunds settled at
// once or later; no window-long interval may hold more than a ceiling.
func TestRollingBudgetIsAStrictCeilingOverEveryWindow(t *testing.T) {
	const window = time.Hour
	limits := LimitsSettings{
		Window:                  window,
		TenantLLMCallsPerWindow: 40,
		TenantTokensPerWindow:   40_000,
		GlobalLLMCallsPerWindow: 90,
		GlobalTokensPerWindow:   70_000,
		MaxLLMCallsPerDocument:  10,
		MaxTokensPerDocument:    10_000,
		GlobalReservePercent:    20,
	}
	g, now := newTestGovernor(limits)
	rng := rand.New(rand.NewPCG(96, 3))
	tenants := []string{"a", "b", "c"}

	type pending struct {
		ticket  *spendTicket
		event   int
		settle  time.Time
		charged int64
	}
	var events []spendEvent
	var open []pending
	end := now.Add(8 * window)
	for now.Before(end) {
		*now = now.Add(time.Duration(1+rng.IntN(300)) * time.Second)
		kept := open[:0]
		for _, p := range open {
			if now.Before(p.settle) {
				kept = append(kept, p)
				continue
			}
			p.ticket.settle(p.charged)
			events[p.event].charged = p.charged
		}
		open = kept
		for range 1 + rng.IntN(6) {
			tenant := tenants[rng.IntN(len(tenants))]
			estimate := int64(1 + rng.IntN(3_000))
			ticket, ok := g.admit(tenant, estimate)
			if !ok {
				continue
			}
			events = append(events, spendEvent{at: *now, tenant: tenant, charged: estimate})
			charged := int64(rng.IntN(int(estimate) + 1))
			if rng.IntN(2) == 0 {
				ticket.settle(charged)
				events[len(events)-1].charged = charged
				continue
			}
			open = append(open, pending{ticket: ticket, event: len(events) - 1, settle: now.Add(time.Duration(rng.IntN(20)) * time.Minute), charged: charged})
		}
	}
	require.Greater(t, len(events), 300, "the run exercises many admissions")

	calls, tokens := maxInAnyWindow(events, window, func(spendEvent) bool { return true })
	require.LessOrEqual(t, calls, limits.GlobalLLMCallsPerWindow, "global calls in one window")
	require.LessOrEqual(t, tokens, limits.GlobalTokensPerWindow, "global tokens in one window")
	for _, tenant := range tenants {
		calls, tokens := maxInAnyWindow(events, window, func(e spendEvent) bool { return e.tenant == tenant })
		require.LessOrEqual(t, calls, limits.TenantLLMCallsPerWindow, "tenant %s calls in one window", tenant)
		require.LessOrEqual(t, tokens, limits.TenantTokensPerWindow, "tenant %s tokens in one window", tenant)
	}
}

// TestRollingBudgetSpentAtOnceStaysSpentForAWindow is the simplest form of the
// finding: a budget spent at once must refuse for the rest of the window, and
// is whole again once that spend is more than a window (plus one slot) old.
func TestRollingBudgetSpentAtOnceStaysSpentForAWindow(t *testing.T) {
	g, now := newTestGovernor(LimitsSettings{TenantLLMCallsPerWindow: 4, TenantTokensPerWindow: 400, Window: time.Hour})
	for range 4 {
		ticket, ok := g.admit("a", 100)
		require.True(t, ok)
		ticket.settle(100)
	}
	for _, elapsed := range []time.Duration{time.Minute, 30 * time.Minute, 59 * time.Minute, time.Hour} {
		at := now.Add(elapsed)
		g.now = func() time.Time { return at }
		require.True(t, g.wouldRefuse("a"), "the spend still counts %s later", elapsed)
	}
	at := now.Add(time.Hour + time.Hour/spendSlots)
	g.now = func() time.Time { return at }
	for range 4 {
		_, ok := g.admit("a", 100)
		require.True(t, ok, "the spend is released once it is more than a window and one slot old")
	}
}

// TestRollingBudgetReleasesSpendInAdmissionOrder checks that spend leaves the
// window in the order it was admitted: what was admitted later is released later.
func TestRollingBudgetReleasesSpendInAdmissionOrder(t *testing.T) {
	g, now := newTestGovernor(LimitsSettings{TenantLLMCallsPerWindow: 4, Window: time.Hour})
	start := *now
	admitN := func(n int) int {
		admitted := 0
		for range n {
			if ticket, ok := g.admit("a", 1); ok {
				ticket.settle(1)
				admitted++
			}
		}
		return admitted
	}
	require.Equal(t, 2, admitN(2))
	*now = start.Add(30 * time.Minute)
	require.Equal(t, 2, admitN(4), "two calls are left in the window")
	*now = start.Add(61 * time.Minute)
	require.Equal(t, 2, admitN(4), "the first two calls left the window")
	*now = start.Add(91 * time.Minute)
	require.Equal(t, 2, admitN(4), "then the next two")
}

// TestRollingBudgetRefundsTheAdmissionSlot checks settlement: a refund frees the
// tokens the reservation held at once, and a charge above the reservation counts
// from its settlement for a whole window, so a slow call's debt is not released
// with its reservation.
func TestRollingBudgetRefundsTheAdmissionSlot(t *testing.T) {
	g, _ := newTestGovernor(LimitsSettings{TenantTokensPerWindow: 1000, Window: time.Hour})
	ticket, ok := g.admit("a", 900)
	require.True(t, ok)
	_, ok = g.admit("a", 200)
	require.False(t, ok, "the reservation holds 900 tokens")
	ticket.settle(100)
	refunded, ok := g.admit("a", 900)
	require.True(t, ok, "the refund frees 800 tokens at once")
	refunded.settle(900)

	g2, now2 := newTestGovernor(LimitsSettings{TenantTokensPerWindow: 1000, Window: time.Hour})
	slow, ok := g2.admit("a", 600)
	require.True(t, ok)
	*now2 = now2.Add(30 * time.Minute)
	slow.settle(1200)
	require.True(t, g2.wouldRefuse("a"), "a budget in debt admits nothing")
	*now2 = now2.Add(31 * time.Minute)
	_, ok = g2.admit("a", 401)
	require.False(t, ok, "the 600-token debt still counts after the reservation left the window")
	debtor, ok := g2.admit("a", 400)
	require.True(t, ok)
	debtor.settle(400)
	*now2 = now2.Add(30 * time.Minute)
	_, ok = g2.admit("a", 600)
	require.True(t, ok, "the debt leaves the window one window after it was charged")
}
