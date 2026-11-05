package throttle

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced clock for every budget of a throttle.
type fakeClock struct{ current time.Time }

// Now returns the clock's current instant.
func (c *fakeClock) Now() time.Time { return c.current }

// advance moves the clock forward by d.
func (c *fakeClock) advance(d time.Duration) { c.current = c.current.Add(d) }

// newTestThrottle builds a throttle on a fake clock, so no budget refills unless
// the test advances it.
func newTestThrottle(t *testing.T, cfg *TelegramThrottleCfg) (*TelegramThrottle, *fakeClock) {
	t.Helper()
	th, err := NewTelegramThrottle(t.Context(), cfg)
	require.NoError(t, err)
	clock := &fakeClock{current: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)}
	th.now = clock.Now
	th.total.last = clock.current
	th.lastSweep = clock.current
	return th, clock
}

// throttleGoroutines counts goroutines started by this package or running a
// go-utils rate limiter. It reads every goroutine's stack, so unlike a total
// goroutine count it does not move with unrelated runtime activity.
func throttleGoroutines(t *testing.T) int {
	t.Helper()
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "go-utils/v6.(*RateLimiter)") ||
			strings.Contains(stack, "created by github.com/Laisky/laisky-blog-graphql/library/throttle.") {
			count++
		}
	}
	return count
}

// TestTelegramThrottleCfgValidate keeps the fail-fast configuration contract
// without building a throttle.
func TestTelegramThrottleCfgValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *TelegramThrottleCfg
		want string
	}{
		{"nil", nil, "config is nil"},
		{"zero total rate", &TelegramThrottleCfg{TotalNPerSec: 0, TotalBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1}, "NPerSec must bigger than 0"},
		{"zero title rate", &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 1, EachTitleNPerSec: 0, EachTitleBurst: 1}, "NPerSec must bigger than 0"},
		{"total burst below rate", &TelegramThrottleCfg{TotalNPerSec: 2, TotalBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1}, "burst must bigger than NPerSec"},
		{"title burst below rate", &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 1, EachTitleNPerSec: 2, EachTitleBurst: 1}, "burst must bigger than NPerSec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, tc.cfg.Validate(), tc.want)
			th, err := NewTelegramThrottle(t.Context(), tc.cfg)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, th)
		})
	}

	require.NoError(t, (&TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1}).Validate())
}

// TestNewTelegramThrottleStartsNoGoroutine pins that a throttle needs no
// lifecycle: building one, even on a context that never ends, starts nothing.
func TestNewTelegramThrottleStartsNoGoroutine(t *testing.T) {
	before := throttleGoroutines(t)
	for range 100 {
		th, err := NewTelegramThrottle(context.Background(), &TelegramThrottleCfg{TotalNPerSec: 10, TotalBurst: 20, EachTitleNPerSec: 5, EachTitleBurst: 10})
		require.NoError(t, err)
		require.True(t, th.Allow("alert-type"))
	}
	require.Equal(t, before, throttleGoroutines(t))
}

// TestAdmitKeepsNoStateForUntrustedTypes reproduces the leak at the library
// boundary: every distinct unauthenticated type string used to get its own
// limiter and refill goroutine. Once their lookups fail, none may keep state.
func TestAdmitKeepsNoStateForUntrustedTypes(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 5000, EachTitleNPerSec: 1, EachTitleBurst: 1})

	before := throttleGoroutines(t)
	for i := range 2000 {
		admission, err := th.Admit(fmt.Sprintf("probe-%04d", i))
		require.NoError(t, err)
		admission.Cancel()
	}

	require.Empty(t, th.titles)
	require.Equal(t, 0, th.Stats().RetainedTitles)
	require.Equal(t, before, throttleGoroutines(t))
}

// TestAdmitRefusedBySharedBudgetSpendsNoTitleBudget pins that a send the
// shared budget refuses leaves the alert type's own budget intact.
func TestAdmitRefusedBySharedBudgetSpendsNoTitleBudget(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 2, EachTitleNPerSec: 1, EachTitleBurst: 3})
	require.True(t, th.Allow("known_type"))
	require.InDelta(t, 2, th.titles["known_type"].tokens, tokenEpsilon)

	require.True(t, th.Allow("other_type"))
	_, err := th.Admit("known_type")
	require.ErrorIs(t, err, ErrTotalBudgetExhausted)

	require.InDelta(t, 2, th.titles["known_type"].tokens, tokenEpsilon)
}

// TestAdmitRefusesExhaustedTitleWithoutSpendingSharedBudget keeps one noisy
// alert type from draining the budget shared by every other type.
func TestAdmitRefusesExhaustedTitleWithoutSpendingSharedBudget(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 2, EachTitleNPerSec: 1, EachTitleBurst: 1})
	require.True(t, th.Allow("noisy_type"))

	for range 10 {
		_, err := th.Admit("noisy_type")
		require.ErrorIs(t, err, ErrTitleBudgetExhausted)
	}
	require.True(t, th.Allow("quiet_type"), "refusing the exhausted type must not spend the shared send")
	_, err := th.Admit("third_type")
	require.ErrorIs(t, err, ErrTotalBudgetExhausted)
}

// TestAdmitReservesConcurrentSendsOfOneType reproduces the review's P2 at the
// library boundary: concurrent requests for one type used to pass the per-type
// check before any of them spent it, so each spent a shared send. Admit reserves
// both sends in one step, so the shared budget pays only for sends the type has.
func TestAdmitReservesConcurrentSendsOfOneType(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 100, EachTitleNPerSec: 1, EachTitleBurst: 5})

	start := make(chan struct{})
	admitted := make(chan *Admission, 200)
	var wg sync.WaitGroup
	for range cap(admitted) {
		wg.Go(func() {
			<-start
			if admission, err := th.Admit("storm_type"); err == nil {
				admitted <- admission
			}
		})
	}
	close(start)
	wg.Wait()
	close(admitted)

	count := 0
	for admission := range admitted {
		require.NoError(t, admission.Authorize("storm_type"))
		count++
	}
	require.Equal(t, 5, count)
	require.InDelta(t, 95, th.total.tokens, tokenEpsilon, "only admitted sends may spend the shared budget")
}

// TestCancelReturnsReservedSend pins that failed authentications never drain a
// type's budget, however many arrive, while each still pays its shared send.
func TestCancelReturnsReservedSend(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 10, EachTitleNPerSec: 1, EachTitleBurst: 2})
	require.True(t, th.Allow("known_type"))

	for range 5 {
		admission, err := th.Admit("known_type")
		require.NoError(t, err)
		admission.Cancel()
	}
	require.InDelta(t, 1, th.titles["known_type"].tokens, tokenEpsilon)
	require.InDelta(t, 4, th.total.tokens, tokenEpsilon)
	require.True(t, th.Allow("known_type"))

	stats := th.Stats()
	require.Equal(t, uint64(7), stats.Admitted)
	require.Equal(t, uint64(2), stats.Authorized)
	require.Equal(t, uint64(5), stats.Canceled)
}

// TestAdmissionSettlesOnce keeps Cancel safe to defer and refuses a second Authorize.
func TestAdmissionSettlesOnce(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 10, EachTitleNPerSec: 1, EachTitleBurst: 2})

	admission, err := th.Admit("known_type")
	require.NoError(t, err)
	require.NoError(t, admission.Authorize("known_type"))
	admission.Cancel()
	require.InDelta(t, 1, th.titles["known_type"].tokens, tokenEpsilon, "Cancel after Authorize must not refund")
	require.ErrorIs(t, admission.Authorize("known_type"), ErrAdmissionSettled)

	canceled, err := th.Admit("known_type")
	require.NoError(t, err)
	canceled.Cancel()
	canceled.Cancel()
	require.ErrorIs(t, canceled.Authorize("known_type"), ErrAdmissionSettled)
	require.InDelta(t, 1, th.titles["known_type"].tokens, tokenEpsilon, "a second Cancel must not refund again")

	var none *Admission
	require.NotPanics(t, none.Cancel)
}

// TestAuthorizeMovesReservationToStoredName covers a lookup that matched a
// differently spelled stored name: the stored name pays, the admitted one does not.
func TestAuthorizeMovesReservationToStoredName(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 10, EachTitleNPerSec: 1, EachTitleBurst: 1})

	admission, err := th.Admit("Known_Type")
	require.NoError(t, err)
	require.NoError(t, admission.Authorize("known_type"))
	require.NotContains(t, th.titles, "Known_Type")
	require.InDelta(t, 0, th.titles["known_type"].tokens, tokenEpsilon)

	again, err := th.Admit("KNOWN_TYPE")
	require.NoError(t, err)
	require.ErrorIs(t, again.Authorize("known_type"), ErrTitleBudgetExhausted)
	require.NotContains(t, th.titles, "KNOWN_TYPE")
}

// TestBudgetsRefillFromElapsedTime checks the goroutine-free refill of both
// budgets: they start full, accrue at the configured rate and cap at the burst.
func TestBudgetsRefillFromElapsedTime(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 100, TotalBurst: 100, EachTitleNPerSec: 3, EachTitleBurst: 6})

	for range 6 {
		require.True(t, th.Allow("known_type"))
	}
	require.False(t, th.Allow("known_type"))

	clock.advance(time.Second / 3)
	require.True(t, th.Allow("known_type"), "a third of a second earns exactly one send at 3/s")
	require.False(t, th.Allow("known_type"))

	clock.advance(time.Hour)
	for range 6 {
		require.True(t, th.Allow("known_type"))
	}
	require.False(t, th.Allow("known_type"), "refill must stop at the burst")

	clock.advance(-time.Minute)
	require.False(t, th.Allow("known_type"), "a clock step backwards must not mint sends")

	shared, sharedClock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 2, TotalBurst: 4, EachTitleNPerSec: 100, EachTitleBurst: 100})
	for i := range 4 {
		require.True(t, shared.Allow(fmt.Sprintf("type-%d", i)), "the shared budget starts at its burst")
	}
	require.False(t, shared.Allow("type-4"))
	sharedClock.advance(time.Second / 2)
	require.True(t, shared.Allow("type-4"), "half a second earns one shared send at 2/s")
	require.False(t, shared.Allow("type-5"))
}

// TestFullBudgetsAreDroppedWithoutChangingDecisions reproduces the review's
// retention finding: every authenticated name kept a budget for the life of the
// process. A budget is now dropped once it has refilled fully, which is the same
// as a missing one, while a budget still refilling is kept.
func TestFullBudgetsAreDroppedWithoutChangingDecisions(t *testing.T) {
	// An empty per-type budget takes four seconds to refill.
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 100, TotalBurst: 1000, EachTitleNPerSec: 1, EachTitleBurst: 4})
	require.Equal(t, 4*time.Second, th.sweepEvery)

	for range 4 {
		require.True(t, th.Allow("drained_type"))
	}
	require.True(t, th.Allow("touched_type"))

	clock.advance(3 * time.Second)
	for range 3 {
		require.True(t, th.Allow("recent_type"))
	}
	require.Equal(t, 3, th.Stats().RetainedTitles, "no sweep is due yet")

	clock.advance(time.Second)
	require.True(t, th.Allow("trigger_type"))
	require.Equal(t, 2, th.Stats().RetainedTitles, "only the budgets still refilling remain")
	require.NotContains(t, th.titles, "drained_type")
	require.NotContains(t, th.titles, "touched_type")
	require.Contains(t, th.titles, "recent_type")

	for range 4 {
		require.True(t, th.Allow("drained_type"), "a dropped budget is a full one")
	}
	require.False(t, th.Allow("drained_type"))
	for range 2 {
		require.True(t, th.Allow("recent_type"), "a kept budget is not reset")
	}
	require.False(t, th.Allow("recent_type"))
}

// TestRetainedBudgetsAreBoundedByRecentSends pins the memory bound: after a
// burst of distinct authenticated names goes quiet, their budgets are dropped.
func TestRetainedBudgetsAreBoundedByRecentSends(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1000, TotalBurst: 1000, EachTitleNPerSec: 1, EachTitleBurst: 2})
	for i := range 1000 {
		require.True(t, th.Allow(fmt.Sprintf("type-%04d", i)))
	}
	require.Equal(t, 1000, th.Stats().RetainedTitles)

	clock.advance(th.sweepEvery)
	require.True(t, th.Allow("next_type"))
	require.Equal(t, 1, th.Stats().RetainedTitles)
}

// TestCanceledReservationOutlivedByRefill covers a lookup slower than a full
// refill: the budget may be dropped meanwhile, and the late Cancel must not
// credit a newer budget of the same name.
func TestCanceledReservationOutlivedByRefill(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 100, TotalBurst: 100, EachTitleNPerSec: 1, EachTitleBurst: 1})

	slow, err := th.Admit("known_type")
	require.NoError(t, err)
	clock.advance(th.sweepEvery)
	require.True(t, th.Allow("other_type"))
	require.NotContains(t, th.titles, "known_type", "the refilled budget is dropped")

	require.True(t, th.Allow("known_type"))
	slow.Cancel()
	_, err = th.Admit("known_type")
	require.ErrorIs(t, err, ErrTitleBudgetExhausted, "the late refund must not credit the newer budget")
}

// remainingSends returns name's per-type sends; a dropped budget is a full one.
func remainingSends(th *TelegramThrottle, name string) float64 {
	th.mu.Lock()
	defer th.mu.Unlock()
	if budget, ok := th.titles[name]; ok {
		return budget.tokens
	}
	return th.titleBurst
}

// TestAdmitIsSafeForConcurrentUse never admits more sends than the budgets
// hold while admissions are authorized and canceled concurrently.
func TestAdmitIsSafeForConcurrentUse(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 1000, EachTitleNPerSec: 1, EachTitleBurst: 50})

	var mu sync.Mutex
	var authorized int
	var unexpected []error
	var wg sync.WaitGroup
	for i := range 400 {
		wg.Go(func() {
			admission, err := th.Admit("known_type")
			if err != nil {
				if !errors.Is(err, ErrTitleBudgetExhausted) {
					mu.Lock()
					unexpected = append(unexpected, err)
					mu.Unlock()
				}
				return
			}
			if i%2 == 0 {
				admission.Cancel()
				return
			}
			err = admission.Authorize("known_type")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unexpected = append(unexpected, err)
				return
			}
			authorized++
		})
	}
	wg.Wait()

	require.Empty(t, unexpected)
	require.Positive(t, authorized)
	require.InDelta(t, 50, float64(authorized)+remainingSends(th, "known_type"), tokenEpsilon)
	stats := th.Stats()
	require.Equal(t, stats.Admitted, stats.Authorized+stats.Canceled)
}

// TestTelegramThrottleCfgDeprecatedSpelling keeps configurations written with
// the historical Totle* field names working, and refuses two spellings that
// disagree instead of silently choosing one.
func TestTelegramThrottleCfgDeprecatedSpelling(t *testing.T) {
	legacy := &TelegramThrottleCfg{TotleNPerSec: 2, TotleBurst: 3, EachTitleNPerSec: 1, EachTitleBurst: 1}
	require.NoError(t, legacy.Validate())
	th, err := NewTelegramThrottle(t.Context(), legacy)
	require.NoError(t, err)
	require.InDelta(t, 2, th.totalRate, tokenEpsilon)
	require.InDelta(t, 3, th.totalBurst, tokenEpsilon)

	same := &TelegramThrottleCfg{TotalNPerSec: 2, TotleNPerSec: 2, TotalBurst: 3, EachTitleNPerSec: 1, EachTitleBurst: 1}
	require.NoError(t, same.Validate())

	conflict := &TelegramThrottleCfg{TotalNPerSec: 2, TotleNPerSec: 5, TotalBurst: 5, EachTitleNPerSec: 1, EachTitleBurst: 1}
	require.ErrorContains(t, conflict.Validate(), "TotalNPerSec is 2 but its deprecated spelling is 5")
	_, err = NewTelegramThrottle(t.Context(), conflict)
	require.ErrorContains(t, err, "deprecated spelling")

	burstConflict := &TelegramThrottleCfg{TotalNPerSec: 1, TotalBurst: 2, TotleBurst: 4, EachTitleNPerSec: 1, EachTitleBurst: 1}
	require.ErrorContains(t, burstConflict.Validate(), "TotalBurst is 2 but its deprecated spelling is 4")
}

func TestLateCancelDoesNotMintTypeTokens(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 100, TotalBurst: 100, EachTitleNPerSec: 1, EachTitleBurst: 2})
	slow, err := th.Admit("known_type")
	require.NoError(t, err)
	clock.advance(time.Second)
	require.True(t, th.Allow("known_type"))
	slow.Cancel()
	require.True(t, th.Allow("known_type"))
	require.False(t, th.Allow("known_type"))
}

func TestOverlappingReservationsCancelOutOfOrder(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotalNPerSec: 100, TotalBurst: 100, EachTitleNPerSec: 1, EachTitleBurst: 3})
	first, err := th.Admit("known_type")
	require.NoError(t, err)
	clock.advance(time.Second / 2)
	second, err := th.Admit("known_type")
	require.NoError(t, err)
	clock.advance(time.Second / 2)
	require.True(t, th.Allow("known_type"))
	second.Cancel()
	first.Cancel()
	require.True(t, th.Allow("known_type"))
	require.False(t, th.Allow("known_type"))
}
