package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// slowAuthority accepts every key; keys with the "sk-slow-" prefix hold their
// upstream slot until release is closed, like a one-api that stopped answering.
type slowAuthority struct {
	started atomic.Int64
	release chan struct{}
}

// ValidateAPIKey blocks slow keys until release, ignoring cancellation, so the
// slot stays occupied for the whole test phase.
func (a *slowAuthority) ValidateAPIKey(_ context.Context, apiKey string) error {
	a.started.Add(1)
	if strings.HasPrefix(apiKey, "sk-slow-") {
		<-a.release
	}
	return nil
}

// TestSlotWaitersThatNeverStartKeepTheirBudget reproduces review finding B:
// with every upstream slot held by a slow one-api call, callers that time out
// waiting for a slot never contact one-api and therefore must not spend the
// per-source or global budget. The clock is frozen, so no budget refills.
func TestSlotWaitersThatNeverStartKeepTheirBudget(t *testing.T) {
	authority := &slowAuthority{release: make(chan struct{})}
	authenticator, _ := newScriptedAuthenticatorFor(t, authority, AuthenticatorOptions{
		MaxConcurrent: 8, UpstreamBurst: 10, PerSourceBurst: 100, Timeout: 100 * time.Millisecond,
	})
	ctx := clientip.WithIP(context.Background(), "198.51.100.40")

	var holders sync.WaitGroup
	for i := range 8 {
		holders.Go(func() {
			_, _ = authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-slow-%d", i))
		})
	}
	require.Eventually(t, func() bool { return authority.started.Load() == 8 }, 5*time.Second, time.Millisecond)

	var waiters sync.WaitGroup
	for i := range 5 {
		waiters.Go(func() {
			_, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-waiter-%d", i))
			require.ErrorIs(t, err, ErrUpstreamThrottled, "a caller without a slot gets a retryable refusal")
		})
	}
	waiters.Wait()
	require.Equal(t, int64(8), authority.started.Load(), "no waiter reached one-api")

	close(authority.release)
	holders.Wait()

	// 10 global tokens, 8 spent by the calls that ran: exactly 2 remain.
	for i := range 2 {
		_, err := authenticator.Authenticate(ctx, fmt.Sprintf("Bearer sk-after-%d", i))
		require.NoError(t, err, "the budget unspent by timed-out waiters must still admit new keys")
	}
	_, err := authenticator.Authenticate(ctx, "Bearer sk-after-over")
	require.ErrorIs(t, err, ErrUpstreamThrottled, "the global budget still bounds upstream calls")
	require.Equal(t, int64(10), authority.started.Load())
}

// TestRevalidationSlotWaitersKeepTheReserve applies the same rule to the
// reserved revalidation budget, at the gate itself.
func TestRevalidationSlotWaitersKeepTheReserve(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	gate := newUpstreamGate(normalizeOptions(AuthenticatorOptions{
		MaxConcurrent: 2, UpstreamBurst: 1, PerSourceBurst: 1, RevalidationBurst: 4, Now: clock.Now,
	}))
	ctx := clientip.WithIP(context.Background(), "198.51.100.41")

	var releases []func()
	for range 2 {
		release, err := gate.admit(ctx, callRevalidation)
		require.NoError(t, err)
		releases = append(releases, release)
	}
	for range 3 {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		_, err := gate.admit(waitCtx, callRevalidation)
		cancel()
		require.ErrorIs(t, err, ErrUpstreamThrottled)
	}
	for _, release := range releases {
		release()
	}

	// Reserve 4 - 2 = 2, then the general budgets' single token.
	for i := range 3 {
		release, err := gate.admit(ctx, callRevalidation)
		require.NoError(t, err, "admission %d", i)
		release()
	}
	_, err := gate.admit(ctx, callRevalidation)
	require.ErrorIs(t, err, ErrUpstreamThrottled)
}

// TestSpentBudgetRefusesWithoutWaitingForASlot keeps the fast refusal: when
// the budgets are already empty, a caller is refused at once instead of
// queuing for a slot it could not use.
func TestSpentBudgetRefusesWithoutWaitingForASlot(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	gate := newUpstreamGate(normalizeOptions(AuthenticatorOptions{
		MaxConcurrent: 1, UpstreamBurst: 1, PerSourceBurst: 5, Now: clock.Now,
	}))
	ctx := clientip.WithIP(context.Background(), "198.51.100.42")
	release, err := gate.admit(ctx, callNewKey)
	require.NoError(t, err)
	defer release()

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	_, err = gate.admit(waitCtx, callNewKey)
	require.ErrorIs(t, err, ErrUpstreamThrottled)
	require.Less(t, time.Since(started), time.Second, "a spent budget must not wait for a slot")
}

// newScriptedAuthenticatorFor builds an authenticator over any validator with
// a frozen fake clock.
func newScriptedAuthenticatorFor(t *testing.T, validator Validator, opts AuthenticatorOptions) (*Authenticator, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	opts.Now = clock.Now
	authenticator, err := NewAuthenticator(validator, opts)
	require.NoError(t, err)
	return authenticator, clock
}
