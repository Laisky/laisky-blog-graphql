package controller

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAuthChallengeTrackerConcurrentGuessesAreBounded reproduces review
// finding 2: guesses that are all admitted before any of their (slow) password
// checks finishes must still respect the account threshold. Only three of 50
// concurrent guesses from 50 addresses may proceed unchallenged.
func TestAuthChallengeTrackerConcurrentGuessesAreBounded(t *testing.T) {
	tracker := newAuthChallengeTracker(time.Minute, 5, 15*time.Minute, 3, 10000)
	const n = 50
	var admitted atomic.Int32
	var inCheck, done sync.WaitGroup
	inCheck.Add(n)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			subject := tracker.subject(fmt.Sprintf("203.0.113.%d", i), "victim@example.com")
			tracker.recordAttempt(subject.client)
			decision := tracker.admit(subject)
			if decision.admitted {
				admitted.Add(1)
			}
			inCheck.Done()
			inCheck.Wait() // every password check is in flight at once
			if decision.admitted {
				tracker.recordFailureFor(subject, "")
				tracker.release(decision.reservation)
			}
		}()
	}
	done.Wait()

	require.EqualValues(t, 3, admitted.Load(), "concurrent guesses must reserve the account budget at admission")
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.1", "victim@example.com")))
	for _, account := range tracker.accounts {
		require.Zero(t, account.inflight, "every reservation is released")
	}
}

// TestAuthChallengeTrackerInfraOutcomeReleasesWithoutCounting verifies a
// request whose credential check could not run releases its reservation
// without counting as a failure.
func TestAuthChallengeTrackerInfraOutcomeReleasesWithoutCounting(t *testing.T) {
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	for i := range 10 {
		subject := tracker.subject(fmt.Sprintf("203.0.113.%d", i), "outage@example.com")
		decision := tracker.admit(subject)
		require.True(t, decision.admitted, "attempt %d", i)
		tracker.release(decision.reservation)
	}
	require.Empty(t, tracker.accounts, "released records without failures are dropped")
}

// TestAuthChallengeTrackerOneChallengeAdmitsOneGuessAtATime reproduces the
// allowance race of review finding 2: one solved challenge must not admit
// clearance_uses+1 parallel guesses. While the request that solved it is in
// flight the allowance cannot be spent; once it ends without a failure (the
// TOTP prompt) the next step may spend it; a failure revokes it.
func TestAuthChallengeTrackerOneChallengeAdmitsOneGuessAtATime(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	for i := range 3 {
		tracker.recordFailure(tracker.subject(fmt.Sprintf("198.51.100.%d", i), "victim@example.com"))
	}
	attacker := tracker.subject("192.0.2.10", "victim@example.com")
	require.False(t, tracker.admit(attacker).admitted)

	solved := tracker.admitVerified(attacker, "")
	for range tracker.clearanceUses {
		require.False(t, tracker.admit(attacker).admitted, "the allowance cannot be spent while the solving request is in flight")
	}
	tracker.recordFailureFor(attacker, "")
	tracker.release(solved)
	require.False(t, tracker.admit(attacker).admitted, "a failure revokes the allowance")

	owner := tracker.subject("192.0.2.20", "victim@example.com")
	step1 := tracker.admitVerified(owner, "")
	tracker.release(step1) // password accepted, TOTP prompt
	step2 := tracker.admit(owner)
	require.True(t, step2.admitted, "the TOTP step spends the allowance")
	require.False(t, tracker.admit(owner).admitted, "but only one step at a time")
	tracker.release(step2.reservation)
	require.True(t, tracker.admitWithoutChallenge(owner))
}

// TestAccountKeyFoldsCollationVariants reproduces review finding 3: spellings
// an accent-, case- and width-insensitive store collation treats as one
// account must share one budget.
func TestAccountKeyFoldsCollationVariants(t *testing.T) {
	tracker := newAuthChallengeTracker(0, 0, 0, 0, 0)
	base := tracker.accountKey("alice")
	for _, variant := range []string{"ALICE", " Alice ", "alicé", "àlîcë", "ａｌｉｃｅ", "al​ice", "alicé"} {
		require.Equal(t, base, tracker.accountKey(variant), "variant %q", variant)
	}
	require.NotEqual(t, base, tracker.accountKey("alicia"))
	require.NotEqual(t, base, tracker.accountKey("a-lice"), "punctuation stays significant, as in utf8mb4_0900_ai_ci")
	require.Equal(t, tracker.accountKey("strasse@example.com"), tracker.accountKey("straße@example.com"))
}

// TestAuthChallengeTrackerVariantFloodSharesBudget verifies guesses spread
// over collation variants of one (possibly unknown) account escalate together.
func TestAuthChallengeTrackerVariantFloodSharesBudget(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	for i, variant := range []string{"alicé", "àlîcë", "ａｌｉｃｅ"} {
		subject := tracker.subject(fmt.Sprintf("203.0.113.%d", i), variant)
		require.True(t, tracker.admitWithoutChallenge(subject))
		tracker.recordFailure(subject)
	}
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.9", "ALICE")))
}

// TestAuthChallengeTrackerIdentityUnifiesSpellings verifies that once the
// store reveals which account a spelling resolves to, failures through its
// username and its email count against one account budget.
func TestAuthChallengeTrackerIdentityUnifiesSpellings(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	identity := tracker.identityKey("oneapi:42")

	tracker.recordFailureFor(tracker.subject("203.0.113.1", "alice"), identity)
	tracker.recordFailureFor(tracker.subject("203.0.113.2", "alice"), identity)
	email := tracker.subject("203.0.113.3", "alice@example.com")
	require.True(t, tracker.admitWithoutChallenge(email), "the email spelling has not resolved yet")
	tracker.recordFailureFor(email, identity)

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.4", "alice@example.com")))
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.5", "ALICE")))
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.6", "bob")), "other accounts are unaffected")

	tracker.recordSuccessFor(tracker.subject("192.0.2.1", "alice@example.com"), identity)
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.7", "alice")), "a success resets the account identity")
}

// TestAuthChallengeTrackerFloodCannotEvictAttackedAccount reproduces review
// finding 5 with the reviewer's parameters: a flood of junk accounts carrying
// as many failures as the victim must not evict it. Once only protected
// records remain the table saturates and untracked accounts are challenged
// instead (escalation, never lockout), until records expire.
func TestAuthChallengeTrackerFloodCannotEvictAttackedAccount(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newAuthChallengeTracker(time.Minute, 5, 15*time.Minute, 3, 10000)
	tracker.now = func() time.Time { return now }
	tracker.maxAccounts = 64

	victim := tracker.subject("198.51.100.1", "victim@example.com")
	for range 4 {
		tracker.recordFailure(victim)
	}
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.2", "victim@example.com")))

	for i := range 64 {
		now = now.Add(time.Second)
		junk := tracker.subject(fmt.Sprintf("2001:db8:%x::1", i), fmt.Sprintf("junk-%d@example.com", i))
		for range 4 {
			tracker.recordFailure(junk)
		}
		require.LessOrEqual(t, len(tracker.accounts), tracker.maxAccounts)
	}

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.3", "victim@example.com")),
		"the attacked account keeps its budget through the flood")
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.4", "fresh@example.com")),
		"while saturated, an account that cannot be tracked is challenged")

	now = now.Add(16 * time.Minute)
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.5", "fresh@example.com")),
		"saturation clears once records expire")
}

// TestAuthChallengeTrackerFloodPrefersBelowThresholdEvictions verifies that
// when room is needed, accounts below the threshold go first, ranked by their
// true failure count.
func TestAuthChallengeTrackerFloodPrefersBelowThresholdEvictions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	tracker.maxAccounts = 16

	victim := tracker.subject("198.51.100.1", "victim@example.com")
	for range 3 {
		tracker.recordFailure(victim)
	}
	two := tracker.subject("198.51.100.2", "two@example.com")
	tracker.recordFailure(two)
	tracker.recordFailure(two)
	for i := range 200 {
		now = now.Add(time.Second)
		tracker.recordFailure(tracker.subject("203.0.113.9", fmt.Sprintf("once-%d@example.com", i)))
		require.LessOrEqual(t, len(tracker.accounts), tracker.maxAccounts)
	}

	require.Contains(t, tracker.accounts, victim.account)
	require.Contains(t, tracker.accounts, two.account, "an account with two failures outranks single-failure junk")
	require.False(t, tracker.accountsSaturated, "single-failure records are always evictable")
}
