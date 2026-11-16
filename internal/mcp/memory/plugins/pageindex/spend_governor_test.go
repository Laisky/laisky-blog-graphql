package pageindex

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestGovernor builds a governor with a controllable clock.
func newTestGovernor(limits LimitsSettings) (*SpendGovernor, *time.Time) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	g := NewSpendGovernorWithClock(limits, func() time.Time { return now }, nil)
	return g, &now
}

// TestSpendGovernorTenantAndGlobalCeilings checks that each tenant has its own
// call ceiling and that the global ceiling holds across tenants.
func TestSpendGovernorTenantAndGlobalCeilings(t *testing.T) {
	g, _ := newTestGovernor(LimitsSettings{TenantLLMCallsPerWindow: 3, GlobalLLMCallsPerWindow: 5})
	for i := 0; i < 3; i++ {
		_, ok := g.admit("a", 10)
		require.True(t, ok)
	}
	_, ok := g.admit("a", 10)
	require.False(t, ok, "tenant a spent its window")
	require.True(t, g.wouldRefuse("a"))

	for i := 0; i < 2; i++ {
		_, ok := g.admit("b", 10)
		require.True(t, ok, "tenant b has its own budget")
	}
	_, ok = g.admit("b", 10)
	require.False(t, ok, "the global ceiling holds across tenants")
	_, ok = g.admit("fresh", 10)
	require.False(t, ok, "a new tenant identity cannot bypass the global ceiling")

	stats := g.Stats()
	require.Equal(t, uint64(5), stats.Admitted)
	require.Equal(t, uint64(2), stats.TenantRefusals, "the cheap pre-check is counted like any refusal")
	require.Equal(t, uint64(2), stats.GlobalRefusals)
}

// TestSpendGovernorTokenCeilingAndDebt checks estimates against the token budget
// and that a call charged above its estimate leaves the budget in debt until the
// charge leaves the window.
func TestSpendGovernorTokenCeilingAndDebt(t *testing.T) {
	g, now := newTestGovernor(LimitsSettings{TenantTokensPerWindow: 1000, Window: time.Hour})
	_, ok := g.admit("a", 1001)
	require.False(t, ok, "an estimate above the window cannot be admitted")

	ticket, ok := g.admit("a", 600)
	require.True(t, ok)
	ticket.settle(1200)
	require.True(t, g.wouldRefuse("a"), "a budget in debt admits nothing")

	*now = now.Add(40 * time.Minute)
	require.True(t, g.wouldRefuse("a"), "the 1200-token charge counts for the whole window")
	*now = now.Add(21 * time.Minute)
	_, ok = g.admit("a", 1000)
	require.True(t, ok, "the whole ceiling is available once the charge left the window")
}

// TestSpendGovernorBoundsTrackedTenants checks the cardinality cap: an untracked
// tenant is refused rather than given a fresh budget, and refilled tenants are
// dropped so the table never exceeds its cap.
func TestSpendGovernorBoundsTrackedTenants(t *testing.T) {
	g, now := newTestGovernor(LimitsSettings{MaxTrackedTenants: 2, Window: time.Hour})
	for _, tenant := range []string{"a", "b"} {
		ticket, ok := g.admit(tenant, 1)
		require.True(t, ok)
		ticket.settle(1)
	}
	_, ok := g.admit("c", 1)
	require.False(t, ok)
	require.Equal(t, uint64(1), g.Stats().UntrackedRefusals)
	require.Equal(t, 2, g.Stats().TrackedTenants)

	*now = now.Add(2 * time.Hour)
	_, ok = g.admit("c", 1)
	require.True(t, ok, "fully refilled budgets are dropped, which changes no decision")
	require.LessOrEqual(t, g.Stats().TrackedTenants, 2)
}

// TestSpendGovernorConcurrentAdmissionsNeverExceedCeiling checks the ceiling
// under concurrent admissions.
func TestSpendGovernorConcurrentAdmissionsNeverExceedCeiling(t *testing.T) {
	g, _ := newTestGovernor(LimitsSettings{TenantLLMCallsPerWindow: 100})
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if ticket, ok := g.admit("a", 5); ok {
					admitted.Add(1)
					ticket.settle(5)
				}
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(100), admitted.Load())
}

// TestOperationBudgetReservation checks the per-operation gate: a reservation
// claims a call, its input and the largest output allowance that fits, refuses
// when less than the minimum output would fit, and a release restores both.
func TestOperationBudgetReservation(t *testing.T) {
	b := newOperationBudget(100, 2, 3, "tenant")
	require.Equal(t, "tenant", b.Tenant())
	_, ok := b.reserveUpTo(101, 0, 0)
	require.False(t, ok, "the token ceiling holds before the call is sent")
	out, ok := b.reserveUpTo(40, 50, 10)
	require.True(t, ok)
	require.Equal(t, int64(50), out)
	b.release(90)
	require.Equal(t, int64(100), b.Remaining())
	out, ok = b.reserveUpTo(40, 100, 10)
	require.True(t, ok)
	require.Equal(t, int64(60), out, "the output allowance shrinks to what the input leaves")
	b.settle(100, 80)
	require.Equal(t, int64(20), b.Remaining())
	_, ok = b.reserveUpTo(15, 100, 10)
	require.False(t, ok, "a call that cannot be granted the minimum output is refused")
	out, ok = b.reserveUpTo(10, 100, 10)
	require.True(t, ok)
	require.Equal(t, int64(10), out)
	_, ok = b.reserveUpTo(0, 0, 0)
	require.False(t, ok, "the call ceiling holds")
	require.True(t, b.exhausted())

	require.True(t, b.takeNodes(3))
	require.False(t, b.takeNodes(1))
	require.Equal(t, 0, b.nodesLeft())
}

// TestLimitsDefaultsCannotBeDisabled checks that zero or negative limits select
// the documented defaults rather than switching a ceiling off.
func TestLimitsDefaultsCannotBeDisabled(t *testing.T) {
	limits := LimitsSettings{MaxLLMCallsPerDocument: -1, MaxTokensPerDocument: 0}.WithDefaults()
	require.Equal(t, DefaultMaxLLMCallsPerDocument, limits.MaxLLMCallsPerDocument)
	require.Equal(t, DefaultMaxTokensPerDocument, limits.MaxTokensPerDocument)
	require.Equal(t, DefaultMaxNodesPerDocument, limits.MaxNodesPerDocument)
	require.Equal(t, DefaultBudgetWindow, limits.Window)
	require.Equal(t, int64(500), limits.documentTokenCeiling(500), "a smaller pipeline allowance still applies")
	require.Equal(t, DefaultMaxTokensPerDocument, limits.documentTokenCeiling(1<<40), "the absolute ceiling caps a large allowance")
}

// TestLocalNodeSummaryIsBounded checks the deterministic local summary.
func TestLocalNodeSummaryIsBounded(t *testing.T) {
	require.Equal(t, "Section 3 Unique body explains one topic.", localNodeSummary("## Section 3\n\nUnique body explains one topic.\n"))
	require.Equal(t, "", localNodeSummary("#\n\n"))
	long := localNodeSummary("# Title\n\n" + strings.Repeat("word ", 1000))
	require.LessOrEqual(t, len(long), localSummaryMaxBytes)
	require.NotEmpty(t, long)
	runOn := localNodeSummary(strings.Repeat("中", 5000))
	require.LessOrEqual(t, len(runOn), localSummaryMaxBytes)
	require.NotEmpty(t, runOn)
}
