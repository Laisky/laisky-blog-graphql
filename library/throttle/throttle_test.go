package throttle

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is a manually advanced clock for the per-title budgets.
type fakeClock struct{ current time.Time }

// Now returns the clock's current instant.
func (c *fakeClock) Now() time.Time { return c.current }

// newTestThrottle builds a throttle whose shared limiter stops with the test.
// The shared limiter refills on wall time, so tests pick rates at which it does
// not refill while they run.
func newTestThrottle(t *testing.T, cfg *TelegramThrottleCfg) (*TelegramThrottle, *fakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	th, err := NewTelegramThrottle(ctx, cfg)
	require.NoError(t, err)
	clock := &fakeClock{current: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)}
	th.now = clock.Now
	return th, clock
}

// TestNewTelegramThrottleRejectsInvalidConfig keeps the fail-fast configuration contract.
func TestNewTelegramThrottleRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  TelegramThrottleCfg
	}{
		{"zero total rate", TelegramThrottleCfg{TotleNPerSec: 0, TotleBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1}},
		{"zero title rate", TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 0, EachTitleBurst: 1}},
		{"total burst below rate", TelegramThrottleCfg{TotleNPerSec: 2, TotleBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1}},
		{"title burst below rate", TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 2, EachTitleBurst: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			th, err := NewTelegramThrottle(context.Background(), &cfg)
			require.Error(t, err)
			require.Nil(t, th)
		})
	}
}

// TestAllowAttemptKeepsNoStateForUntrustedTypes reproduces the leak at the library
// boundary: every distinct unauthenticated type string used to get its own
// limiter and refill goroutine. Neither may grow now.
func TestAllowAttemptKeepsNoStateForUntrustedTypes(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotleNPerSec: 100, TotleBurst: 100, EachTitleNPerSec: 1, EachTitleBurst: 1})

	before := runtime.NumGoroutine()
	for i := range 2000 {
		th.AllowAttempt(fmt.Sprintf("probe-%04d", i))
	}

	require.Empty(t, th.titles)
	require.Less(t, runtime.NumGoroutine()-before, 20)
}

// TestAllowAttemptRefusedBySharedBudgetSpendsNoTitleBudget pins that a send the
// shared budget refuses leaves the alert type's own budget intact.
func TestAllowAttemptRefusedBySharedBudgetSpendsNoTitleBudget(t *testing.T) {
	// One shared send, refilled after a second.
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 2, EachTitleBurst: 2})
	require.True(t, th.AllowAuthorized("known_type"))
	require.InDelta(t, 1, th.titles["known_type"].tokens, tokenEpsilon)

	require.True(t, th.AllowAttempt("other_type"))
	require.False(t, th.AllowAttempt("known_type"), "the shared budget is empty")

	require.InDelta(t, 1, th.titles["known_type"].tokens, tokenEpsilon)
	require.True(t, th.AllowAuthorized("known_type"))
}

// TestAllowAttemptRefusesExhaustedTitleWithoutSpendingSharedBudget keeps one
// noisy alert type from draining the budget shared by every other type.
func TestAllowAttemptRefusesExhaustedTitleWithoutSpendingSharedBudget(t *testing.T) {
	// One shared send, refilled after a second.
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 1, EachTitleBurst: 1})
	require.True(t, th.AllowAuthorized("noisy_type"))
	require.False(t, th.AllowAuthorized("noisy_type"))

	for range 10 {
		require.False(t, th.AllowAttempt("noisy_type"))
	}
	require.True(t, th.AllowAttempt("quiet_type"), "refusing the exhausted type must not spend the shared send")
}

// TestAllowAuthorizedRefillsFromElapsedTime checks the goroutine-free refill:
// sends accrue at the configured rate, cap at the burst and start at one second's worth.
func TestAllowAuthorizedRefillsFromElapsedTime(t *testing.T) {
	th, clock := newTestThrottle(t, &TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 3, EachTitleBurst: 6})

	for range 3 {
		require.True(t, th.AllowAuthorized("known_type"))
	}
	require.False(t, th.AllowAuthorized("known_type"))

	clock.current = clock.current.Add(time.Second / 3)
	require.True(t, th.AllowAuthorized("known_type"), "a third of a second earns exactly one send at 3/s")
	require.False(t, th.AllowAuthorized("known_type"))

	clock.current = clock.current.Add(time.Hour)
	for range 6 {
		require.True(t, th.AllowAuthorized("known_type"))
	}
	require.False(t, th.AllowAuthorized("known_type"), "refill must stop at the burst")

	clock.current = clock.current.Add(-time.Minute)
	require.False(t, th.AllowAuthorized("known_type"), "a clock step backwards must not mint sends")
}

// TestAllowAuthorizedIsSafeForConcurrentUse never admits more sends than the budget holds.
func TestAllowAuthorizedIsSafeForConcurrentUse(t *testing.T) {
	th, _ := newTestThrottle(t, &TelegramThrottleCfg{TotleNPerSec: 1, TotleBurst: 1, EachTitleNPerSec: 50, EachTitleBurst: 50})

	admitted := make(chan bool, 200)
	start := make(chan struct{})
	for range cap(admitted) {
		go func() {
			<-start
			admitted <- th.AllowAuthorized("known_type")
		}()
	}
	close(start)

	total := 0
	for range cap(admitted) {
		if <-admitted {
			total++
		}
	}
	require.Equal(t, 50, total)
}
