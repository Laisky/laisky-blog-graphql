package controller

import (
	"testing"
	"time"

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

// highRiskTrackerForUnknown returns a tracker that immediately demands a
// challenge for the unknown-client bucket used by background-context requests.
func highRiskTrackerForUnknown() *authChallengeTracker {
	tracker := newAuthChallengeTracker(time.Minute, 100, 15*time.Minute, 1, 100)
	tracker.recordFailure(unknownAuthClientKey)
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

	tracker.recordFailure("client")
	require.False(t, tracker.challengeRequired("client"))

	tracker.recordFailure("client")
	require.True(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerSuccessResetsFailures(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)

	tracker.recordFailure("client")
	tracker.recordFailure("client")
	require.True(t, tracker.challengeRequired("client"))

	tracker.recordSuccess("client")
	require.False(t, tracker.challengeRequired("client"))
}

func TestAuthChallengeTrackerFailureWindowExpiry(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := newTestTracker(time.Minute, 100, 15*time.Minute, 2, &now)

	tracker.recordFailure("client")
	tracker.recordFailure("client")
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
