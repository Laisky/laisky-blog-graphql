package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// withAuthChallengeTracker swaps the process-wide risk tracker for the duration
// of a test and restores the previous instance afterwards.
func withAuthChallengeTracker(t *testing.T, tracker *authChallengeTracker) {
	t.Helper()

	authChallengeMu.Lock()
	prev := authChallengeInst
	authChallengeInst = tracker
	authChallengeMu.Unlock()

	t.Cleanup(func() {
		authChallengeMu.Lock()
		authChallengeInst = prev
		authChallengeMu.Unlock()
	})
}

// recordFailure records a credential failure that revealed no store identity.
func (t *authChallengeTracker) recordFailure(subject authRiskSubject) {
	t.recordFailureFor(subject, "")
}

// recordSuccess records a successful authentication without a store identity.
func (t *authChallengeTracker) recordSuccess(subject authRiskSubject) {
	t.recordSuccessFor(subject, "")
}

// admitWithoutChallenge admits one request with the account signal applied
// and immediately releases its reservations, the sequential-request view.
func (t *authChallengeTracker) admitWithoutChallenge(subject authRiskSubject) bool {
	decision := t.admit(subject, true)
	t.release(decision.reservation)
	return decision.admitted
}

// recordChallengePassed admits a request carrying a verified token and
// immediately releases it, granting the allowance.
func (t *authChallengeTracker) recordChallengePassed(subject authRiskSubject) {
	t.release(t.admitVerified(subject, ""))
}

// clientSubject returns a risk subject for a flow that names no account.
func clientSubject(clientKey string) authRiskSubject {
	return authRiskSubject{client: clientKey}
}

// highRiskTrackerForUnknown returns a tracker that immediately demands a
// challenge for the unknown-client bucket used by background-context requests.
func highRiskTrackerForUnknown() *authChallengeTracker {
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 1, 100)
	tracker.recordFailure(clientSubject(unknownAuthClientKey))
	return tracker
}

// newTestTracker builds a tracker with a fixed clock the caller can advance.
func newTestTracker(
	frequencyWindow time.Duration,
	frequencyThreshold int,
	failureWindow time.Duration,
	failureThreshold int,
	now *time.Time,
) *authChallengeTracker {
	tracker := newAuthChallengeTracker(frequencyWindow, frequencyThreshold, failureWindow, failureThreshold, 100)
	tracker.now = func() time.Time { return *now }
	return tracker
}

func TestAuthChallengeTrackerFrequency(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 3, 15*time.Minute, 100, &now)

	// Up to and including the threshold, no challenge is demanded.
	for range 3 {
		tracker.recordAttempt("client")
	}
	require.False(t, tracker.challengeRequired("client"))

	// One more attempt exceeds the frequency threshold.
	tracker.recordAttempt("client")
	require.True(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerFrequencyWindowExpiry(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 3, 15*time.Minute, 100, &now)

	for range 4 {
		tracker.recordAttempt("client")
	}
	require.True(t, tracker.challengeRequired("client"))

	// After the frequency window elapses the attempts age out.
	now = now.Add(2 * time.Minute)
	require.False(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerFailures(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)

	tracker.recordFailure(clientSubject("client"))
	require.False(t, tracker.challengeRequired("client"))

	tracker.recordFailure(clientSubject("client"))
	require.True(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerSuccessResetsFailures(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)

	tracker.recordFailure(clientSubject("client"))
	tracker.recordFailure(clientSubject("client"))
	require.True(t, tracker.challengeRequired("client"))

	tracker.recordSuccess(clientSubject("client"))
	require.False(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerFailureWindowExpiry(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)

	tracker.recordFailure(clientSubject("client"))
	tracker.recordFailure(clientSubject("client"))
	require.True(t, tracker.challengeRequired("client"))

	now = now.Add(20 * time.Minute)
	require.False(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerUnknownClientNotChallenged(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 3, 15*time.Minute, 2, &now)

	require.False(t, tracker.challengeRequired("never-seen"))
}

func TestAuthChallengeTrackerDefaultsAppliedForNonPositiveConfig(t *testing.T) {
	tracker := newAuthChallengeTracker(0, 0, 0, 0, 0)
	require.Equal(t, defaultTurnstileFrequencyWindow, tracker.frequencyWindow)
	require.Equal(t, defaultTurnstileFrequencyThreshold, tracker.frequencyThreshold)
	require.Equal(t, defaultTurnstileFailureWindow, tracker.failureWindow)
	require.Equal(t, defaultTurnstileFailureThreshold, tracker.failureThreshold)
	require.Equal(t, defaultTurnstileMaxTrackedClients, tracker.maxClients)
}

// TestAuthChallengeTrackerSolvedChallengeAllowance verifies a solved challenge
// admits a bounded number of follow-up requests (such as the TOTP step) and is
// revoked by the next credential failure.
func TestAuthChallengeTrackerSolvedChallengeAllowance(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)
	tracker.clearanceUses = 2

	tracker.recordFailure(clientSubject("client"))
	tracker.recordFailure(clientSubject("client"))
	require.True(t, tracker.challengeRequired("client"))
	require.False(t, tracker.admitWithoutChallenge(clientSubject("client")))

	tracker.recordChallengePassed(clientSubject("client"))
	require.False(t, tracker.challengeRequired("client"), "a fresh allowance is not consumed by a query")
	require.True(t, tracker.admitWithoutChallenge(clientSubject("client")))
	require.True(t, tracker.admitWithoutChallenge(clientSubject("client")))
	require.False(t, tracker.admitWithoutChallenge(clientSubject("client")), "the allowance is bounded")
	require.True(t, tracker.challengeRequired("client"))

	tracker.recordChallengePassed(clientSubject("client"))
	tracker.recordFailure(clientSubject("client"))
	require.False(t, tracker.admitWithoutChallenge(clientSubject("client")), "a credential failure revokes the allowance")

	tracker.recordChallengePassed(clientSubject("client"))
	now = now.Add(defaultTurnstileClearanceTTL + time.Second)
	require.False(t, tracker.admitWithoutChallenge(clientSubject("client")), "the allowance expires")
}

// TestAuthChallengeTrackerLowRiskAdmitted verifies low-risk clients never need
// a challenge and never consume an allowance.
func TestAuthChallengeTrackerLowRiskAdmitted(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 3, 15*time.Minute, 2, &now)

	require.True(t, tracker.admitWithoutChallenge(clientSubject("never-seen")))
	tracker.recordAttempt("client")
	require.True(t, tracker.admitWithoutChallenge(clientSubject("client")))
}

// challengeRequired reports whether the client's next auth request would have
// to solve a Turnstile challenge: it is high risk (recent request frequency or
// accumulated failures) and holds no unused solved-challenge allowance. It does
// not spend the allowance.
func (t *authChallengeTracker) challengeRequired(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, ok := t.clients[key]
	if !ok {
		return false
	}
	now := t.now()
	return t.highRiskLocked(key, client, now) && !client.hasClearance(now)
}

// TestAuthChallengeTrackerAccountBudgetSurvivesClientRotation verifies that
// failures against one account accumulate across client addresses, so an
// attacker rotating addresses is challenged once the account reaches its
// threshold, while other accounts and account-less flows stay unchallenged.
func TestAuthChallengeTrackerAccountBudgetSurvivesClientRotation(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)

	for i, variant := range []string{"victim@example.test", " Victim@Example.TEST ", "VICTIM@example.test"} {
		subject := tracker.subject(fmt.Sprintf("203.0.113.%d", i+1), variant)
		tracker.recordAttempt(subject.client)
		require.True(t, tracker.admitWithoutChallenge(subject), "attempt %d is below the threshold", i+1)
		tracker.recordFailure(subject)
	}

	fresh := tracker.subject("203.0.113.50", "victim@example.test")
	tracker.recordAttempt(fresh.client)
	require.False(t, tracker.admitWithoutChallenge(fresh), "a fresh address must not escape the account's failure budget")
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.51", "other@example.test")))
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("203.0.113.52", "")), "flows naming no account are unaffected")
}

// TestAuthChallengeTrackerAccountBudgetClearance verifies a legitimate user
// of an attacked account passes after one challenge, keeps the bounded
// allowance for the multi-step flow, loses it on a credential failure, and
// resets the account budget on success.
func TestAuthChallengeTrackerAccountBudgetClearance(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	tracker.clearanceUses = 2
	for i := range 3 {
		tracker.recordFailure(tracker.subject(fmt.Sprintf("198.51.100.%d", i+1), "owner@example.test"))
	}

	owner := tracker.subject("192.0.2.10", "owner@example.test")
	require.False(t, tracker.admitWithoutChallenge(owner))
	tracker.recordChallengePassed(owner)
	require.True(t, tracker.admitWithoutChallenge(owner), "the solved challenge admits the TOTP step")
	require.True(t, tracker.admitWithoutChallenge(owner))
	require.False(t, tracker.admitWithoutChallenge(owner), "the allowance is bounded")

	tracker.recordChallengePassed(owner)
	tracker.recordFailure(owner)
	require.False(t, tracker.admitWithoutChallenge(owner), "a credential failure revokes the allowance")

	tracker.recordSuccess(owner)
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("192.0.2.11", "owner@example.test")),
		"a successful authentication resets the account budget")
}

// TestAuthChallengeTrackerAccountBudgetExpires verifies account failures age
// out with the failure window and the record is dropped.
func TestAuthChallengeTrackerAccountBudgetExpires(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)
	tracker.recordFailure(tracker.subject("198.51.100.1", "a@example.test"))
	tracker.recordFailure(tracker.subject("198.51.100.2", "a@example.test"))
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.3", "a@example.test")))

	now = now.Add(16 * time.Minute)
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.3", "a@example.test")))
	require.Empty(t, tracker.accounts)
}

// TestAuthChallengeTrackerAccountThresholdConfigurable verifies the account
// threshold can differ from the per-client threshold.
func TestAuthChallengeTrackerAccountThresholdConfigurable(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	require.Equal(t, 3, tracker.accountFailureThreshold, "the account threshold defaults to the client threshold")
	tracker.accountFailureThreshold = 5
	for i := range 4 {
		tracker.recordFailure(tracker.subject(fmt.Sprintf("198.51.100.%d", i+1), "b@example.test"))
	}
	require.True(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.9", "b@example.test")))
	tracker.recordFailure(tracker.subject("198.51.100.5", "b@example.test"))
	require.False(t, tracker.admitWithoutChallenge(tracker.subject("198.51.100.10", "b@example.test")))
}

// TestAuthChallengeTrackerAccountKeysAreKeyedHashes verifies the tracker never
// stores account names and its keys differ per process.
func TestAuthChallengeTrackerAccountKeysAreKeyedHashes(t *testing.T) {
	first := newAuthChallengeTracker(0, 0, 0, 0, 0)
	second := newAuthChallengeTracker(0, 0, 0, 0, 0)

	key := first.accountKey("Secret.Owner@example.test")
	require.Len(t, key, 2*accountKeyBytes)
	require.NotContains(t, key, "secret")
	require.Equal(t, key, first.accountKey(" secret.owner@EXAMPLE.test"))
	require.NotEqual(t, key, second.accountKey("secret.owner@example.test"), "keys must not be precomputable across processes")
	require.Empty(t, first.accountKey("   "))

	first.recordFailure(first.subject("198.51.100.1", "secret.owner@example.test"))
	for stored := range first.accounts {
		require.NotContains(t, stored, "owner")
	}
}

// TestAuthChallengeTrackerAccountTableBounded verifies a flood of failures
// against unique (for example non-existent) accounts cannot grow memory past
// the bound, and cannot evict an account under sustained attack.
func TestAuthChallengeTrackerAccountTableBounded(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	tracker.maxAccounts = 100
	for i := range 3 {
		tracker.recordFailure(tracker.subject(fmt.Sprintf("198.51.100.%d", i+1), "target@example.test"))
	}

	for i := range 20_000 {
		now = now.Add(time.Millisecond)
		tracker.recordFailure(tracker.subject("203.0.113.1", fmt.Sprintf("ghost-%d@example.test", i)))
		require.LessOrEqual(t, len(tracker.accounts), tracker.maxAccounts)
	}

	require.False(t, tracker.admitWithoutChallenge(tracker.subject("192.0.2.1", "target@example.test")),
		"the attacked account keeps its failure budget through the flood")
}

// TestAuthChallengeTrackerClientTableBounded verifies a flood of unique client
// addresses inside the signal windows cannot grow the client table past
// max_tracked_clients, and cannot evict a client at its failure threshold.
func TestAuthChallengeTrackerClientTableBounded(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 3, &now)
	tracker.maxClients = 100
	for range 3 {
		tracker.recordFailure(clientSubject("198.51.100.200"))
	}

	for i := range 20_000 {
		tracker.recordAttempt(fmt.Sprintf("flood-%d", i))
		require.LessOrEqual(t, len(tracker.clients), tracker.maxClients)
	}

	require.True(t, tracker.challengeRequired("198.51.100.200"), "a high-risk client survives the flood")
}

// TestGetAuthChallengeTrackerReadsAccountSettings verifies the account budget
// settings are read from configuration.
func TestGetAuthChallengeTrackerReadsAccountSettings(t *testing.T) {
	base := "settings.web.turnstile.risk"
	for key, value := range map[string]any{
		base + ".account_failure_threshold": 7,
		base + ".max_tracked_accounts":      123,
		base + ".failure_threshold":         4,
	} {
		previous := gconfig.Shared.Get(key)
		gconfig.Shared.Set(key, value)
		t.Cleanup(func() { gconfig.Shared.Set(key, previous) })
	}
	withAuthChallengeTracker(t, nil)

	tracker := getAuthChallengeTracker()
	require.Equal(t, 7, tracker.accountFailureThreshold)
	require.Equal(t, 123, tracker.maxAccounts)
	require.Equal(t, 4, tracker.failureThreshold)

	gconfig.Shared.Set(base+".account_failure_threshold", 0)
	withAuthChallengeTracker(t, nil)
	require.Equal(t, 4, getAuthChallengeTracker().accountFailureThreshold, "unset falls back to failure_threshold")
}

// TestAuthChallengeTrackerAccountKeyBoundsInput verifies an oversized account
// is hashed over a bounded prefix, so the gate's work per request is bounded.
func TestAuthChallengeTrackerAccountKeyBoundsInput(t *testing.T) {
	tracker := newAuthChallengeTracker(0, 0, 0, 0, 0)
	long := strings.Repeat("a", maxAccountKeyInput)
	require.Equal(t, tracker.accountKey(long), tracker.accountKey(long+strings.Repeat("b", 1<<20)))
	require.NotEqual(t, tracker.accountKey("a@example.test"), tracker.accountKey("b@example.test"))
}
