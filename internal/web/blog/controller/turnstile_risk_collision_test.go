package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collisionTracker returns a tracker with a fixed clock and a spelling that two
// distinct store accounts share: "alicé@example.test" and "alice@example.test"
// fold to one key, but a store comparing exact (lower-cased) strings, as the
// MongoDB login lookup does, keeps them as two accounts.
func collisionTracker(t *testing.T) (*authChallengeTracker, string, string) {
	t.Helper()
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	require.Equal(t, tracker.accountKey("alicé@example.test"), tracker.accountKey("alice@example.test"),
		"the fixture needs two spellings that fold together")
	return tracker, tracker.identityKey("mongo:victim"), tracker.identityKey("mongo:owner")
}

// TestAuthSuccessKeepsCollidingIdentityEnforced reproduces review P2 "account
// folding can group different MongoDB identities": a sign-in to one account
// must not lower challenge enforcement on a different account whose spelling
// folds to the same key.
func TestAuthSuccessKeepsCollidingIdentityEnforced(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alicé@example.test"), victim)
	}
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.1", "alicé@example.test")))

	tracker.recordSuccessFor(tracker.subject("192.0.2.1", "alice@example.test"), owner)

	for i := range 5 {
		decision := tracker.admit(tracker.subject(fmt.Sprintf("198.51.100.%d", 10+i), "alicé@example.test"))
		require.False(t, decision.admitted, "guess %d against the attacked account must still be challenged", i)
	}
}

// TestAuthSuccessAfterAliasFlipKeepsCollidingIdentityEnforced covers the
// variant where a failure of the colliding account re-points the shared
// spelling to itself before it signs in.
func TestAuthSuccessAfterAliasFlipKeepsCollidingIdentityEnforced(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alicé@example.test"), victim)
	}
	tracker.recordFailureFor(tracker.subject("192.0.2.1", "alice@example.test"), owner)
	tracker.recordSuccessFor(tracker.subject("192.0.2.1", "alice@example.test"), owner)

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.20", "alicé@example.test")),
		"the attacked account must stay linked to the shared spelling")
}

// TestAuthSuccessOfOneCollidingIdentityKeepsTheOtherEnforced is the mirror
// case: the account under attack is the one that does not sign in.
func TestAuthSuccessOfOneCollidingIdentityKeepsTheOtherEnforced(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alice@example.test"), owner)
	}
	tracker.recordFailureFor(tracker.subject("192.0.2.2", "alicé@example.test"), victim)
	tracker.recordSuccessFor(tracker.subject("192.0.2.2", "alicé@example.test"), victim)

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.30", "alice@example.test")),
		"a sign-in to one colliding account must not orphan the other account's failures")
}

// TestAuthCollisionDoesNotGrantConcurrentFreeGuesses checks the concurrent
// form of the attack: after the colliding sign-in, a burst of guesses against
// the attacked account from fresh addresses must not reach the credential check.
func TestAuthCollisionDoesNotGrantConcurrentFreeGuesses(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alicé@example.test"), victim)
	}
	tracker.recordSuccessFor(tracker.subject("192.0.2.3", "alice@example.test"), owner)

	var held []authReservation
	admitted := 0
	for i := range 10 {
		decision := tracker.admit(tracker.subject(fmt.Sprintf("198.51.100.%d", 40+i), "alicé@example.test"))
		if decision.admitted {
			admitted++
			held = append(held, decision.reservation)
		}
	}
	for _, reservation := range held {
		tracker.release(reservation)
	}
	require.Zero(t, admitted, "no guess may skip the challenge while the attacked account is over its threshold")
}

// TestAuthSuccessStillResetsAnUncontestedSpelling is the control for the
// collision fix: without a colliding account, a sign-in still clears the
// spelling it used, so a legitimate user's typos are forgiven.
func TestAuthSuccessStillResetsAnUncontestedSpelling(t *testing.T) {
	tracker, _, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alice@example.test"), owner)
	}
	tracker.recordFailureFor(tracker.subject("203.0.113.9", "alice@example.test"), "")
	tracker.recordSuccessFor(tracker.subject("192.0.2.4", "alice@example.test"), owner)

	require.True(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.50", "alice@example.test")))
	require.Empty(t, tracker.accounts, "an uncontested success leaves no account records behind")
}

// TestSpellingIdentityLinksAreBounded verifies a spelling links at most
// maxSpellingIdentities identities, keeping the most recently seen ones.
func TestSpellingIdentityLinksAreBounded(t *testing.T) {
	tracker, _, _ := collisionTracker(t)
	subject := tracker.subject("203.0.113.1", "alice@example.test")
	var identities []string
	for i := range maxSpellingIdentities + 2 {
		identity := tracker.identityKey(fmt.Sprintf("mongo:%d", i))
		identities = append(identities, identity)
		tracker.recordFailureFor(subject, identity)
	}
	tracker.recordFailureFor(subject, identities[2]) // seen again: most recent
	require.Equal(t, []string{identities[3], identities[4], identities[5], identities[2]}, tracker.accounts[subject.account].identities)
}

// TestUnrevealedFailureCountsAgainstEveryLinkedIdentity verifies a failure
// whose store revealed no identity (a wrong email code is checked before the
// account is loaded) counts against every identity its spelling links, and
// that admission reserves a slot on each of them.
func TestUnrevealedFailureCountsAgainstEveryLinkedIdentity(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	tracker.recordFailureFor(tracker.subject("203.0.113.1", "alicé@example.test"), victim)
	tracker.recordFailureFor(tracker.subject("203.0.113.2", "alice@example.test"), owner)

	decision := tracker.admit(tracker.subject("203.0.113.3", "alice@example.test"))
	require.True(t, decision.admitted)
	require.ElementsMatch(t, []string{victim, owner}, decision.identities)
	require.Equal(t, 1, tracker.accounts[victim].inflight)
	require.Equal(t, 1, tracker.accounts[owner].inflight)
	tracker.recordFailureLinked(tracker.subject("203.0.113.3", "alice@example.test"), "", decision.identities)
	tracker.release(decision.reservation)

	require.Len(t, tracker.accounts[victim].failures, 2)
	require.Len(t, tracker.accounts[owner].failures, 2)
	require.Zero(t, tracker.accounts[victim].inflight)
}

// TestAuthSuccessIgnoresStaleIdentityIndex verifies a success never clears a
// spelling through a stale index entry: once the indexed spelling record was
// dropped and re-created by failures resolving to another account, it is left
// alone.
func TestAuthSuccessIgnoresStaleIdentityIndex(t *testing.T) {
	tracker, victim, owner := collisionTracker(t)
	email := tracker.subject("203.0.113.1", "owner@example.test")
	tracker.recordFailureFor(email, owner)
	require.Contains(t, tracker.accounts[owner].spellings, email.account)

	delete(tracker.accounts, email.account) // evicted, then re-created below
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", 10+i), "owner@example.test"), victim)
	}
	tracker.recordSuccessFor(tracker.subject("192.0.2.9", "owner-name"), owner)

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.9", "owner@example.test")),
		"a stale index entry must not clear a spelling that now links another identity")
}

// TestAuthSuccessKeepsUnattributedFailuresOfCollidingSpelling reproduces the
// independent-review finding on PR #92's collision fix: wrong email codes are
// rejected before the account is loaded, so they reveal no identity. A
// sign-in through a different exact spelling that folds to the same key must
// not clear them.
func TestAuthSuccessKeepsUnattributedFailuresOfCollidingSpelling(t *testing.T) {
	tracker, _, owner := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alicé@example.test"), "")
	}
	tracker.recordSuccessFor(tracker.subject("192.0.2.5", "alice@example.test"), owner)

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.60", "alicé@example.test")),
		"guesses against another exact spelling must survive a colliding sign-in")
}

// TestAuthSuccessClearsOwnUnattributedTypos is the control for the test above:
// unattributed failures from the exact spelling that then signs in are the
// user's own typos and are forgiven.
func TestAuthSuccessClearsOwnUnattributedTypos(t *testing.T) {
	tracker, _, owner := collisionTracker(t)
	for i := range 2 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), " Alice@Example.test"), "")
	}
	tracker.recordSuccessFor(tracker.subject("192.0.2.6", "alice@example.test"), owner)
	require.Empty(t, tracker.accounts, "the same store spelling's typos are cleared")
}

// TestAuthLinkOverflowKeepsSpellingEvidence reproduces the bounded-link
// variant: failures of more colliding accounts than a spelling can link push
// the attacked account's link out; signing in to each of them must still not
// clear the shared spelling.
func TestAuthLinkOverflowKeepsSpellingEvidence(t *testing.T) {
	tracker, victim, _ := collisionTracker(t)
	for i := range 3 {
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("203.0.113.%d", i), "alicé@example.test"), victim)
	}
	spellings := []string{"alice@example.test", "àlice@example.test", "alîce@example.test", "alicè@example.test"}
	var colliders []string
	for i := range maxSpellingIdentities {
		collider := tracker.identityKey(fmt.Sprintf("mongo:collider-%d", i))
		colliders = append(colliders, collider)
		tracker.recordFailureFor(tracker.subject(fmt.Sprintf("192.0.2.%d", 20+i), spellings[i]), collider)
	}
	for i, collider := range colliders {
		tracker.recordSuccessFor(tracker.subject(fmt.Sprintf("192.0.2.%d", 20+i), spellings[i]), collider)
	}

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.61", "alicé@example.test")),
		"overflowing the link bound must not let colliding sign-ins clear the attacked account's evidence")
}
