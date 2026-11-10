package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"

	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// Default risk thresholds for demanding a Turnstile challenge. They are tuned so
// that ordinary users never see a challenge: only clients that make bursts of
// requests or accumulate repeated failures, and accounts that accumulate
// repeated failures from any client, are asked to solve one.
const (
	defaultTurnstileFrequencyWindow    = time.Minute
	defaultTurnstileFrequencyThreshold = 5
	defaultTurnstileFailureWindow      = 15 * time.Minute
	defaultTurnstileFailureThreshold   = 3
	defaultTurnstileMaxTrackedClients  = 10000
	defaultTurnstileMaxTrackedAccounts = 10000
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

// authRiskSubject names who an authentication request is attributed to: the
// network client sending it and, for flows that name an account, the account
// spelling it targets. A client rotating addresses gets a fresh client record,
// but every guess against one account lands on the same account records.
type authRiskSubject struct {
	// client is the network identity key (see resolveAuthClientKey).
	client string
	// account is the keyed hash of the folded account spelling, or empty when
	// the flow names no account (GitHub OAuth, passkey and registration flows).
	account string
}

// authChallengeTracker records recent authentication attempts so the SSO flow
// can demand a Turnstile challenge only for suspicious requests: clients making
// frequent requests or accumulating failures, and accounts accumulating
// failures from any client. Low-risk requests authenticate without a challenge.
// It only ever escalates to a challenge; it never locks an account out.
//
// Accounts are tracked twice: per folded spelling, and per store identity once
// a failure revealed which account the spelling resolves to, so username, email
// and case or accent variants of one account share a budget. Admission reserves
// an in-flight slot on each, so concurrent guesses count before their outcome
// is known.
//
// Both tables are bounded (see turnstile_risk_table.go) and accounts are held
// only as keyed hashes.
//
// It is safe for concurrent use.
type authChallengeTracker struct {
	mu       sync.Mutex
	clients  map[string]*clientAuthRisk
	accounts map[string]*accountAuthRisk

	frequencyWindow         time.Duration
	frequencyThreshold      int
	failureWindow           time.Duration
	failureThreshold        int
	accountFailureThreshold int
	maxClients              int
	maxAccounts             int
	clearanceTTL            time.Duration
	clearanceUses           int

	// accountsSaturated is set while the account table is full of records that
	// may not be evicted; untracked accounts are then treated as high risk.
	accountsSaturated bool
	// saturationCheckedAt rate-limits the sweep that clears accountsSaturated.
	saturationCheckedAt time.Time

	// accountHashKey is a per-process random HMAC key, so the account table
	// never holds account names and its keys cannot be matched against a
	// precomputed list of accounts.
	accountHashKey []byte

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
	// clearanceBusy counts in-flight requests admitted by a solved challenge
	// or by spending the allowance. The allowance can only be spent while it
	// is zero, so one solved challenge never admits parallel guesses.
	clearanceBusy int
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

// idle reports whether the record carries no signal and can be dropped.
func (c *clientAuthRisk) idle(now time.Time) bool {
	return len(c.attempts) == 0 && len(c.failures) == 0 && !c.hasClearance(now) && c.clearanceBusy == 0
}

// lastSeen returns the newest signal timestamp of the client.
func (c *clientAuthRisk) lastSeen() time.Time {
	var latest time.Time
	if n := len(c.attempts); n > 0 {
		latest = c.attempts[n-1]
	}
	if n := len(c.failures); n > 0 && c.failures[n-1].After(latest) {
		latest = c.failures[n-1]
	}
	return latest
}

// accountAuthRisk holds the recent credential failures against one account
// spelling or one account identity, from any client.
type accountAuthRisk struct {
	// failures are the timestamps inside the failure window, newest last,
	// capped at the threshold plus one.
	failures []time.Time
	// total counts every failure since the record was created; it ranks
	// records for eviction where the capped window list ties.
	total int
	// inflight counts admitted requests whose outcome is not known yet.
	inflight int
	// alias is the identity key this spelling resolved to at its last failure.
	alias string
}

// idle reports whether the record carries no signal and can be dropped.
func (a *accountAuthRisk) idle() bool {
	return len(a.failures) == 0 && a.inflight == 0
}

// authReservation is what an admission holds until the request's outcome is
// known: in-flight slots on account records and the client's allowance slot.
type authReservation struct {
	accounts []reservedAccount
	client   *clientAuthRisk
}

// reservedAccount is one account record holding an in-flight slot.
type reservedAccount struct {
	key    string
	record *accountAuthRisk
}

// authAdmission is the tracker's decision for one request.
type authAdmission struct {
	// admitted reports whether the request may proceed without a token.
	admitted bool
	// identity is the identity key the account spelling resolved to, if known.
	identity string
	// accountGap reports that the account is high risk but the entry point
	// cannot present a challenge, so only the client signal was applied.
	accountGap bool
	// reservation must be released once the outcome is known.
	reservation authReservation
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

	hashKey := make([]byte, sha256.Size)
	// crypto/rand.Read never returns an error and panics if the system source fails.
	_, _ = rand.Read(hashKey)

	return &authChallengeTracker{
		clients:                 make(map[string]*clientAuthRisk),
		accounts:                make(map[string]*accountAuthRisk),
		frequencyWindow:         frequencyWindow,
		frequencyThreshold:      frequencyThreshold,
		failureWindow:           failureWindow,
		failureThreshold:        failureThreshold,
		accountFailureThreshold: failureThreshold,
		maxClients:              maxClients,
		maxAccounts:             defaultTurnstileMaxTrackedAccounts,
		clearanceTTL:            defaultTurnstileClearanceTTL,
		clearanceUses:           defaultTurnstileClearanceUses,
		accountHashKey:          hashKey,
		now:                     func() time.Time { return gutils.Clock.GetUTCNow() },
	}
}

// subject builds the risk subject for a request from its client key and the
// raw account the request names, which may be empty.
func (t *authChallengeTracker) subject(clientKey string, account string) authRiskSubject {
	return authRiskSubject{client: clientKey, account: t.accountKey(account)}
}

// recordAttempt notes that a client started an authentication request. Every
// auth flow calls this, so it drives the request-frequency signal.
func (t *authChallengeTracker) recordAttempt(clientKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(clientKey, now)
	client.attempts = appendRecent(client.attempts, now, now.Add(-t.frequencyWindow), t.frequencyThreshold+1)
}

// admit decides whether the current auth request may proceed without a
// Turnstile token. A request whose client and account are both low risk
// always may; otherwise it may only by spending the client's solved-challenge
// allowance, and only while no other request admitted by that allowance is in
// flight. The account signal applies only when accountEscalation is true,
// that is when the entry point can carry a token and the host can render the
// widget. An admitted request reserves in-flight slots that the caller must
// release.
func (t *authChallengeTracker) admit(subject authRiskSubject, accountEscalation bool) authAdmission {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	decision := authAdmission{identity: t.aliasLocked(subject.account)}
	client, known := t.clients[subject.client]
	clientRisky := known && t.highRiskLocked(subject.client, client, now)
	accountRisky := t.accountRiskyLocked(subject.account, now) || t.accountRiskyLocked(decision.identity, now)
	if accountRisky && !accountEscalation {
		decision.accountGap = true
		accountRisky = false
	}

	if !clientRisky && !accountRisky {
		decision.admitted = true
		decision.reservation = t.reserveAccountsLocked(now, subject.account, decision.identity)
		return decision
	}

	// highRiskLocked may have dropped an idle record; re-read it.
	client, known = t.clients[subject.client]
	if !known || !client.hasClearance(now) || client.clearanceBusy > 0 {
		return decision
	}
	client.clearanceLeft--
	client.clearanceBusy++
	decision.admitted = true
	decision.reservation = t.reserveAccountsLocked(now, subject.account, decision.identity)
	decision.reservation.client = client
	return decision
}

// admitVerified admits a request that just presented a verified Turnstile
// token and grants the client the bounded allowance: its next clearanceUses
// auth requests within clearanceTTL are admitted without another challenge,
// one at a time, unless a credential failure revokes it first. The returned
// reservation includes the allowance slot this request now occupies.
func (t *authChallengeTracker) admitVerified(subject authRiskSubject, identity string) authReservation {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.clearedUntil = now.Add(t.clearanceTTL)
	client.clearanceLeft = t.clearanceUses
	client.clearanceBusy++

	reservation := t.reserveAccountsLocked(now, subject.account, identity)
	reservation.client = client
	return reservation
}

// release returns the in-flight slots held by an admission.
func (t *authChallengeTracker) release(reservation authReservation) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if reservation.client != nil && reservation.client.clearanceBusy > 0 {
		reservation.client.clearanceBusy--
	}
	for _, reserved := range reservation.accounts {
		if reserved.record.inflight > 0 {
			reserved.record.inflight--
		}
		// The record may have been replaced meanwhile; only drop the one held.
		if reserved.record.idle() && t.accounts[reserved.key] == reserved.record {
			delete(t.accounts, reserved.key)
		}
	}
}

// recordFailureFor notes that a credential check failed. It drives the
// client's and the account's repeated-failure signals and revokes the
// client's solved-challenge allowance, so every further guess from a high-risk
// client, or against a high-risk account, needs a fresh challenge. identity is
// the identity key the store resolved the account to, or empty when the store
// did not reveal it; the spelling then keeps whatever identity it resolved to
// before.
func (t *authChallengeTracker) recordFailureFor(subject authRiskSubject, identity string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.failures = appendRecent(client.failures, now, now.Add(-t.failureWindow), t.failureThreshold+1)
	client.revokeClearance()

	if subject.account == "" {
		return
	}
	if spelling := t.accountLocked(subject.account, now); spelling != nil {
		spelling.failures = appendRecent(spelling.failures, now, now.Add(-t.failureWindow), t.accountFailureThreshold+1)
		spelling.total++
		if identity != "" {
			spelling.alias = identity
		}
		identity = spelling.alias
	}
	if identity == "" {
		return
	}
	if account := t.accountLocked(identity, now); account != nil {
		account.failures = appendRecent(account.failures, now, now.Add(-t.failureWindow), t.accountFailureThreshold+1)
		account.total++
	}
}

// recordSuccessFor clears the failure history of the client, of the account
// spelling and of its identity after a successful authentication, so a
// legitimate user is not challenged for past typos (NIST SP 800-63B-4 §3.2.2
// resets the retry count of the authenticators used once authentication
// succeeds). In-flight slots held by concurrent requests are kept.
func (t *authChallengeTracker) recordSuccessFor(subject authRiskSubject, identity string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if identity == "" {
		identity = t.aliasLocked(subject.account)
	}
	for _, key := range []string{subject.account, identity} {
		account, ok := t.accounts[key]
		if key == "" || !ok {
			continue
		}
		account.failures = nil
		account.total = 0
		account.alias = ""
		if account.idle() {
			delete(t.accounts, key)
		}
	}

	client, ok := t.clients[subject.client]
	if !ok {
		return
	}
	client.failures = nil
	if client.idle(t.now()) {
		delete(t.clients, subject.client)
	}
}

// highRiskLocked prunes expired signals and reports whether the client exceeds
// a risk threshold. The caller must hold the mutex.
func (t *authChallengeTracker) highRiskLocked(key string, client *clientAuthRisk, now time.Time) bool {
	client.attempts = keepRecent(client.attempts, now.Add(-t.frequencyWindow))
	client.failures = keepRecent(client.failures, now.Add(-t.failureWindow))

	if client.idle(now) {
		delete(t.clients, key)
		return false
	}

	return len(client.attempts) > t.frequencyThreshold || len(client.failures) >= t.failureThreshold
}

// accountRiskyLocked prunes expired failures and reports whether the account
// record has reached its threshold, counting requests still in flight. An
// account that cannot be tracked because the table is saturated is treated as
// high risk. The caller must hold the mutex.
func (t *authChallengeTracker) accountRiskyLocked(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	account, ok := t.accounts[key]
	if !ok {
		return t.accountsSaturatedLocked(now)
	}

	account.failures = keepRecent(account.failures, now.Add(-t.failureWindow))
	if account.idle() {
		delete(t.accounts, key)
		return false
	}

	return len(account.failures)+account.inflight >= t.accountFailureThreshold
}

// aliasLocked returns the identity key an account spelling last resolved to.
// The caller must hold the mutex.
func (t *authChallengeTracker) aliasLocked(spelling string) string {
	if spelling == "" {
		return ""
	}
	if account, ok := t.accounts[spelling]; ok {
		return account.alias
	}
	return ""
}

// reserveAccountsLocked takes an in-flight slot on each named account record,
// creating records as needed. A record the saturated table cannot hold is
// skipped. The caller must hold the mutex.
func (t *authChallengeTracker) reserveAccountsLocked(now time.Time, keys ...string) authReservation {
	var reservation authReservation
	for _, key := range keys {
		if key == "" {
			continue
		}
		account := t.accountLocked(key, now)
		if account == nil {
			continue
		}
		account.inflight++
		reservation.accounts = append(reservation.accounts, reservedAccount{key: key, record: account})
	}
	return reservation
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
		authChallengeInst = newAuthChallengeTrackerFromConfig()
	}
	return authChallengeInst
}

// ResetAuthChallengeTracker discards all adaptive-authentication risk state and
// rebuilds the process-wide tracker from the current configuration on next
// use. Tests that drive the tracker through the HTTP engine call it to start
// from a clean state; it is safe to call concurrently with requests.
func ResetAuthChallengeTracker() {
	authChallengeMu.Lock()
	defer authChallengeMu.Unlock()
	authChallengeInst = nil
}

// newAuthChallengeTrackerFromConfig builds a tracker from settings.web.turnstile.risk.
func newAuthChallengeTrackerFromConfig() *authChallengeTracker {
	base := "settings.web.turnstile.risk"
	tracker := newAuthChallengeTracker(
		time.Duration(gconfig.Shared.GetInt(base+".frequency_window_seconds"))*time.Second,
		gconfig.Shared.GetInt(base+".frequency_threshold"),
		time.Duration(gconfig.Shared.GetInt(base+".failure_window_seconds"))*time.Second,
		gconfig.Shared.GetInt(base+".failure_threshold"),
		gconfig.Shared.GetInt(base+".max_tracked_clients"),
	)
	if threshold := gconfig.Shared.GetInt(base + ".account_failure_threshold"); threshold > 0 {
		tracker.accountFailureThreshold = threshold
	}
	if limit := gconfig.Shared.GetInt(base + ".max_tracked_accounts"); limit > 0 {
		tracker.maxAccounts = limit
	}
	if seconds := gconfig.Shared.GetInt(base + ".clearance_seconds"); seconds > 0 {
		tracker.clearanceTTL = time.Duration(seconds) * time.Second
	}
	if uses := gconfig.Shared.GetInt(base + ".clearance_uses"); uses > 0 {
		tracker.clearanceUses = uses
	}
	return tracker
}

// resolveAuthClientKey derives the network identity key for the current
// request from the shared client-identity policy: an IPv4 address, or the /64
// network of an IPv6 address so one host cannot rotate through its prefix.
// Unidentifiable clients share one key.
func resolveAuthClientKey(ctx context.Context) string {
	if key := clientip.NetworkKey(clientip.FromContext(ctx)); key != "" {
		return key
	}
	return unknownAuthClientKey
}

// resolveAuthRiskSubject returns the risk subject of the current request for
// the given raw account, which is empty for flows that name no account.
func resolveAuthRiskSubject(ctx context.Context, tracker *authChallengeTracker, account string) authRiskSubject {
	return tracker.subject(resolveAuthClientKey(ctx), account)
}
