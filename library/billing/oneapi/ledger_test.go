package oneapi

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCallLedgerCountsTheTrailingWindow mirrors one-api's sliding per-IP
// window: a call counts until the window (plus one-api's whole-second
// truncation) has passed.
func TestCallLedgerCountsTheTrailingWindow(t *testing.T) {
	ledger := NewCallLedger(RateLimitWindow)
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	for i := range 5 {
		ledger.Record(start.Add(time.Duration(i)*time.Second), RouteTokenConsume)
	}
	require.Equal(t, 5, ledger.Count(start.Add(4*time.Second)))
	require.Equal(t, 5, ledger.Count(start.Add(RateLimitWindow)), "one-api still counts a call made exactly one window ago")
	require.Equal(t, 4, ledger.Count(start.Add(RateLimitWindow+time.Second)), "the oldest call left the window")
	require.Zero(t, ledger.Count(start.Add(RateLimitWindow+5*time.Second)))
	require.Zero(t, ledger.Count(start.Add(24*time.Hour)), "stale slots never count again")
}

// TestCallLedgerTryRecordRefusesAtTheLimit: the gate's calls stop once the
// window holds limit calls, consumes included, and resume as calls age out.
func TestCallLedgerTryRecordRefusesAtTheLimit(t *testing.T) {
	ledger := NewCallLedger(RateLimitWindow)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	for range 3 {
		ledger.Record(now, RouteTokenConsume)
	}
	require.True(t, ledger.TryRecord(now, RouteTokenBalance, 4))
	require.False(t, ledger.TryRecord(now, RouteTokenBalance, 4), "the fourth call filled the window")
	require.False(t, ledger.Available(now, 4))
	require.True(t, ledger.TryRecord(now, RouteTokenBalance, 0), "a limit of 0 is an exempt egress")
	require.True(t, ledger.Available(now.Add(RateLimitWindow+time.Second), 4))

	var nilLedger *CallLedger
	require.True(t, nilLedger.TryRecord(now, RouteTokenBalance, 1), "no ledger bounds nothing")
	require.True(t, nilLedger.Available(now, 1))
	require.Zero(t, nilLedger.Count(now))
}

// TestCallLedgerTryRecordIsAtomic: concurrent callers never record more than
// the limit.
func TestCallLedgerTryRecordIsAtomic(t *testing.T) {
	ledger := NewCallLedger(RateLimitWindow)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	const limit = 50
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for range 200 {
		wg.Go(func() {
			if ledger.TryRecord(now, RouteTokenBalance, limit) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Equal(t, limit, admitted)
	require.Equal(t, limit, ledger.Count(now))
}
