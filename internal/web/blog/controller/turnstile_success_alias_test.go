package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuthSuccessResetsAllKnownAliases(t *testing.T) {
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	identity := tracker.identityKey("mongo:alice")
	email := tracker.subject("203.0.113.1", "alice@example.test")
	name := tracker.subject("203.0.113.2", "alice")
	other := tracker.subject("203.0.113.3", "unrelated")
	for range 3 {
		tracker.recordFailureFor(email, identity)
		tracker.recordFailureFor(other, tracker.identityKey("mongo:other"))
	}
	tracker.recordSuccessFor(name, identity)
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.1", "alice@example.test")),
		"success using another identifier must clear the authenticated account's known aliases")
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.2", "unrelated")),
		"success must not clear unrelated account risk")
}

func TestAuthSuccessPreservesAliasReservations(t *testing.T) {
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 3, 100)
	identity := tracker.identityKey("mongo:alice")
	email := tracker.subject("203.0.113.1", "alice@example.test")
	tracker.recordFailureFor(email, identity)
	decision := tracker.admit(email)
	require.True(t, decision.admitted)
	tracker.recordSuccessFor(tracker.subject("203.0.113.2", "alice"), identity)
	require.Equal(t, 1, tracker.accounts[email.account].inflight)
	require.Empty(t, tracker.accounts[email.account].failures)
	tracker.recordFailureFor(email, identity)
	tracker.release(decision.reservation)
	require.Len(t, tracker.accounts[email.account].failures, 1, "a later concurrent failure is not erased")
}
