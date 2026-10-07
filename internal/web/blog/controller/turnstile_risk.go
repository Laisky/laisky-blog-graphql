package controller

import (
	"context"
	"sync"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"
)

// Default risk thresholds for demanding a Turnstile challenge. They are tuned so
// that ordinary users never see a challenge: only clients that make bursts of
// requests or accumulate repeated failures are asked to solve one.
const (
	defaultTurnstileFrequencyWindow    = time.Minute
	defaultTurnstileFrequencyThreshold = 5
	defaultTurnstileFailureWindow      = 15 * time.Minute
	defaultTurnstileFailureThreshold   = 3
	defaultTurnstileMaxTrackedClients  = 10000
	// defaultTurnstileClearanceTTL and defaultTurnstileClearanceUses bound the
	// allowance a client earns by solving a challenge: the next few auth
	// requests in this window (for example the TOTP step that follows a
	// verified password) are not challenged again.
	defaultTurnstileClearanceTTL  = 5 * time.Minute
	defaultTurnstileClearanceUses = 3

	// unknownAuthClientKey buckets requests whose client IP cannot be resolved so
	// they are still rate-tracked collectively rather than escaping the check.
	unknownAuthClientKey = "unknown"
)

// authChallengeTracker records recent authentication attempts per client so the
// SSO flow can demand a Turnstile challenge only for suspicious clients: those
// making frequent requests or accumulating repeated failures. Low-risk clients
// authenticate without ever seeing a challenge.
//
// It is safe for concurrent use.
type authChallengeTracker struct {
	mu      sync.Mutex
	clients map[string]*clientAuthRisk

	frequencyWindow    time.Duration
	frequencyThreshold int
	failureWindow      time.Duration
	failureThreshold   int
	maxClients         int
	clearanceTTL       time.Duration
	clearanceUses      int

	// now returns the current time; overridable in tests.
	now func() time.Time
}

// clientAuthRisk holds the recent attempt and failure timestamps for one client,
// plus the allowance earned by its last solved Turnstile challenge.
type clientAuthRisk struct {
	attempts []time.Time
	failures []time.Time

	// clearedUntil and clearanceLeft describe the solved-challenge allowance.
	clearedUntil  time.Time
	clearanceLeft int
}

// hasClearance reports whether the solved-challenge allowance is still usable.
func (c *clientAuthRisk) hasClearance(now time.Time) bool {
	return c.clearanceLeft > 0 && now.Before(c.clearedUntil)
}

// revokeClearance drops any solved-challenge allowance.
func (c *clientAuthRisk) revokeClearance() {
	c.clearedUntil = time.Time{}
	c.clearanceLeft = 0
}

// newAuthChallengeTracker builds a tracker with the provided thresholds,
// falling back to defaults for any non-positive value.
func newAuthChallengeTracker(
	frequencyWindow time.Duration,
	frequencyThreshold int,
	failureWindow time.Duration,
	failureThreshold int,
	maxClients int,
) *authChallengeTracker {
	if frequencyWindow <= 0 {
		frequencyWindow = defaultTurnstileFrequencyWindow
	}
	if frequencyThreshold <= 0 {
		frequencyThreshold = defaultTurnstileFrequencyThreshold
	}
	if failureWindow <= 0 {
		failureWindow = defaultTurnstileFailureWindow
	}
	if failureThreshold <= 0 {
		failureThreshold = defaultTurnstileFailureThreshold
	}
	if maxClients <= 0 {
		maxClients = defaultTurnstileMaxTrackedClients
	}

	return &authChallengeTracker{
		clients:            make(map[string]*clientAuthRisk),
		frequencyWindow:    frequencyWindow,
		frequencyThreshold: frequencyThreshold,
		failureWindow:      failureWindow,
		failureThreshold:   failureThreshold,
		maxClients:         maxClients,
		clearanceTTL:       defaultTurnstileClearanceTTL,
		clearanceUses:      defaultTurnstileClearanceUses,
		now:                func() time.Time { return gutils.Clock.GetUTCNow() },
	}
}

// recordAttempt notes that a client started an authentication request. Every
// auth flow calls this, so it drives the request-frequency signal.
func (t *authChallengeTracker) recordAttempt(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(key)
	client.attempts = appendRecent(client.attempts, now, now.Add(-t.frequencyWindow), t.frequencyThreshold+1)

	if len(t.clients) > t.maxClients {
		t.sweepLocked(now)
	}
}

// recordFailure notes that a client failed a credential check. This drives the
// repeated-failure signal and revokes any solved-challenge allowance, so every
// further guess from a high-risk client needs a fresh challenge.
func (t *authChallengeTracker) recordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(key)
	client.failures = appendRecent(client.failures, now, now.Add(-t.failureWindow), t.failureThreshold+1)
	client.revokeClearance()
}

// recordChallengePassed grants the allowance earned by a verified Turnstile
// token: the next clearanceUses auth requests within clearanceTTL are admitted
// without another challenge unless a credential failure revokes it first.
func (t *authChallengeTracker) recordChallengePassed(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client := t.clientLocked(key)
	client.clearedUntil = t.now().Add(t.clearanceTTL)
	client.clearanceLeft = t.clearanceUses
}

// admitWithoutChallenge decides whether the current auth request may proceed
// without a Turnstile token. Low-risk clients always may. A high-risk client
// may only by spending one unit of its solved-challenge allowance.
func (t *authChallengeTracker) admitWithoutChallenge(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, ok := t.clients[key]
	if !ok {
		return true
	}
	now := t.now()
	if !t.highRiskLocked(key, client, now) {
		return true
	}
	if !client.hasClearance(now) {
		return false
	}
	client.clearanceLeft--
	return true
}

// recordSuccess clears the failure history for a client after a successful
// authentication so a legitimate user is not challenged for past typos.
func (t *authChallengeTracker) recordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, ok := t.clients[key]
	if !ok {
		return
	}
	client.failures = nil
	if len(client.attempts) == 0 {
		delete(t.clients, key)
	}
}

// highRiskLocked prunes expired signals and reports whether the client exceeds
// a risk threshold. The caller must hold the mutex.
func (t *authChallengeTracker) highRiskLocked(key string, client *clientAuthRisk, now time.Time) bool {
	client.attempts = keepRecent(client.attempts, now.Add(-t.frequencyWindow))
	client.failures = keepRecent(client.failures, now.Add(-t.failureWindow))

	if len(client.attempts) == 0 && len(client.failures) == 0 && !client.hasClearance(now) {
		delete(t.clients, key)
		return false
	}

	return len(client.attempts) > t.frequencyThreshold || len(client.failures) >= t.failureThreshold
}

// clientLocked returns the risk record for a key, creating it if absent. The
// caller must hold the mutex.
func (t *authChallengeTracker) clientLocked(key string) *clientAuthRisk {
	client, ok := t.clients[key]
	if !ok {
		client = &clientAuthRisk{}
		t.clients[key] = client
	}
	return client
}

// sweepLocked evicts clients with no recent activity to bound memory. The caller
// must hold the mutex.
func (t *authChallengeTracker) sweepLocked(now time.Time) {
	for key, client := range t.clients {
		client.attempts = keepRecent(client.attempts, now.Add(-t.frequencyWindow))
		client.failures = keepRecent(client.failures, now.Add(-t.failureWindow))
		if len(client.attempts) == 0 && len(client.failures) == 0 && !client.hasClearance(now) {
			delete(t.clients, key)
		}
	}
}

// appendRecent appends now to times, drops entries at or before cutoff, and
// keeps at most the newest max entries.
func appendRecent(times []time.Time, now time.Time, cutoff time.Time, limit int) []time.Time {
	kept := keepRecent(append(times, now), cutoff)
	if limit > 0 && len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}

// keepRecent returns the timestamps strictly after cutoff.
func keepRecent(times []time.Time, cutoff time.Time) []time.Time {
	kept := make([]time.Time, 0, len(times))
	for _, ts := range times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	return kept
}

var (
	authChallengeMu   sync.Mutex
	authChallengeInst *authChallengeTracker
)

// getAuthChallengeTracker returns the process-wide risk tracker, building it
// from configuration on first use. Tests may swap the instance via the mutex.
func getAuthChallengeTracker() *authChallengeTracker {
	authChallengeMu.Lock()
	defer authChallengeMu.Unlock()
	if authChallengeInst == nil {
		base := "settings.web.turnstile.risk"
		authChallengeInst = newAuthChallengeTracker(
			time.Duration(gconfig.Shared.GetInt(base+".frequency_window_seconds"))*time.Second,
			gconfig.Shared.GetInt(base+".frequency_threshold"),
			time.Duration(gconfig.Shared.GetInt(base+".failure_window_seconds"))*time.Second,
			gconfig.Shared.GetInt(base+".failure_threshold"),
			gconfig.Shared.GetInt(base+".max_tracked_clients"),
		)
		if seconds := gconfig.Shared.GetInt(base + ".clearance_seconds"); seconds > 0 {
			authChallengeInst.clearanceTTL = time.Duration(seconds) * time.Second
		}
		if uses := gconfig.Shared.GetInt(base + ".clearance_uses"); uses > 0 {
			authChallengeInst.clearanceUses = uses
		}
	}
	return authChallengeInst
}

// resolveAuthClientKey derives the rate-tracking key for the current request,
// bucketing unidentifiable clients under a shared key.
func resolveAuthClientKey(ctx context.Context) string {
	if ip := resolveTurnstileClientIP(ctx); ip != "" {
		return ip
	}
	return unknownAuthClientKey
}
