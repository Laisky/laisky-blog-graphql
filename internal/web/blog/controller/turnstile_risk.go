package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"slices"
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

	// maxSpellingIdentities bounds how many store identities one spelling
	// record links. More than one only happens when distinct store accounts
	// fold to one spelling (for example "alice" and "alicé" in a store that
	// compares exact strings). Displacing a linked identity takes that many
	// further failures through the spelling, each of which is challenged once
	// the spelling is high risk.
	maxSpellingIdentities = 4
	// maxIdentitySpellings bounds the reverse index from an identity record to
	// the spellings that link it. One account is reachable through a handful
	// of folded spellings (its username and its email); an entry pushed out
	// only means a success no longer clears that spelling early.
	maxIdentitySpellings = 8
)

// authRiskSubject names who an authentication request is attributed to: the
// network client sending it and, for flows that name an account, the account
// spelling it targets. A client rotating addresses gets a fresh client record,
// but every guess against one account lands on the same account records.
type authRiskSubject struct {
	// client is the network identity key (see resolveAuthClientKey).
	client string
	// account is the keyed hash of the folded account spelling, or empty when
	// the flow names no account (GitHub OAuth start, passkey start).
	account string
	// exact is the keyed hash of the account in the form the store looks it
	// up (see exactAccountKey). Several exact spellings may fold to one
	// account key; exact tells them apart when a success decides whose
	// failures it may forgive.
	exact string
}

// authChallengeTracker records recent authentication attempts so the SSO flow
// can demand a Turnstile challenge only for suspicious requests: clients making
// frequent requests or accumulating failures, and accounts accumulating
// failures from any client. Low-risk requests authenticate without a challenge.
// It only ever escalates to a challenge; it never locks an account out.
//
// Accounts are tracked twice: per folded spelling, and per store identity once
// a failure revealed which account the spelling resolves to, so username, email
// and case or accent variants of one account share a budget.
//
// Invariants, all maintained under mu:
//
//   - Admission reads the risk of the spelling and of every identity it links,
//     and reserves an in-flight slot on each, so concurrent guesses count
//     before their outcome is known. Every reservation is released exactly
//     once by authAttempt; release never drops a record a later admission
//     replaced.
//   - The spelling fold may group more inputs than a store treats as one
//     account (see foldAccount), never fewer. The store identity is therefore
//     authoritative: a spelling may link several identities, and a success
//     clears only evidence that belongs to the identity that authenticated. A
//     spelling keeps its failures when it still links another identity, holds
//     identity-less failures from another exact spelling, or overflowed its
//     bounds, so a sign-in to one account never lowers enforcement on a
//     colliding one.
//   - A success clears failure history, never in-flight reservations, so a
//     concurrent guess admitted before the success still counts when it fails.
//   - Each identity record indexes the spellings that link it (spellings), so
//     a success visits only those instead of scanning the table. The index is
//     advisory: an entry may be stale once its spelling record was dropped or
//     re-created, so every entry is re-checked against the spelling's own
//     links before anything is cleared; and an index lost with an evicted
//     identity record only means the success clears just the spelling it
//     used. Neither can clear evidence of another identity.
//   - Solved-challenge clearance is per client and admits one request at a
//     time (clearanceBusy); any credential failure revokes it.
//   - Records at or above their threshold, or with requests in flight, are
//     never evicted (see turnstile_risk_table.go); a table full of them
//     saturates and treats untracked accounts as high risk.
//
// Both tables are bounded and accounts are held only as keyed hashes. The
// state is per process: replicas do not share budgets (see the SSO manual).
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
	// identities are the identity keys this spelling resolved to at its
	// failures, most recent last, at most maxSpellingIdentities. Only
	// spelling records have them.
	identities []string
	// anonymous are the exact-spelling keys of failures that revealed no
	// identity (a wrong email or registration code is rejected before any
	// account is loaded), at most maxSpellingIdentities. Only spelling
	// records have them.
	anonymous []string
	// contested is set once identities or anonymous overflowed their bound:
	// the record may then hold evidence of accounts it no longer names, so no
	// success clears its failures; they expire with the failure window.
	contested bool
	// spellings are the spelling keys that linked this identity, most recent
	// last, at most maxIdentitySpellings. Only identity records have them.
	spellings []string
}

// idle reports whether the record carries no signal and can be dropped.
func (a *accountAuthRisk) idle() bool {
	return len(a.failures) == 0 && a.inflight == 0
}

// clearFailures drops the record's failure history but keeps its in-flight
// reservations.
func (a *accountAuthRisk) clearFailures() {
	a.failures = nil
	a.total = 0
	a.anonymous = nil
}

// link records that the spelling resolved to identity, keeping the most
// recent maxSpellingIdentities distinct identities.
func (a *accountAuthRisk) link(identity string) {
	var overflowed bool
	a.identities, overflowed = appendBoundedUnique(a.identities, identity, maxSpellingIdentities)
	a.contested = a.contested || overflowed
}

// noteAnonymous records that a failure through exact revealed no identity.
func (a *accountAuthRisk) noteAnonymous(exact string) {
	var overflowed bool
	a.anonymous, overflowed = appendBoundedUnique(a.anonymous, exact, maxSpellingIdentities)
	a.contested = a.contested || overflowed
}

// forgivableBy reports whether a success by identity through the exact
// spelling may clear this spelling record: it links no other identity, it was
// never contested, and every failure that revealed no identity came through
// that same exact spelling. exact is empty for a spelling reached through the
// identity index rather than used by the request.
func (a *accountAuthRisk) forgivableBy(identity string, exact string) bool {
	if a.contested {
		return false
	}
	for _, linked := range a.identities {
		if linked != identity {
			return false
		}
	}
	for _, spelling := range a.anonymous {
		if spelling != exact {
			return false
		}
	}
	return true
}

// indexSpelling records that spelling links this identity, keeping the most
// recent maxIdentitySpellings distinct spellings.
func (a *accountAuthRisk) indexSpelling(spelling string) {
	a.spellings, _ = appendBoundedUnique(a.spellings, spelling, maxIdentitySpellings)
}

// appendBoundedUnique moves value to the end of values, dropping the oldest
// entries beyond limit, and reports whether any were dropped. An empty value
// leaves values unchanged.
func appendBoundedUnique(values []string, value string, limit int) ([]string, bool) {
	if value == "" {
		return values, false
	}
	values = slices.DeleteFunc(values, func(existing string) bool { return existing == value })
	values = append(values, value)
	if extra := len(values) - limit; extra > 0 {
		return slices.Delete(values, 0, extra), true
	}
	return values, false
}

// unlink removes identity from the spelling's linked identities.
func (a *accountAuthRisk) unlink(identity string) {
	a.identities = slices.DeleteFunc(a.identities, func(linked string) bool { return linked == identity })
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
	// identities are the identity keys the account spelling linked at
	// admission, if any.
	identities []string
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
	return authRiskSubject{client: clientKey, account: t.accountKey(account), exact: t.exactAccountKey(account)}
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
// flight. An admitted request reserves in-flight slots that the caller must release.
func (t *authChallengeTracker) admit(subject authRiskSubject) authAdmission {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	decision := authAdmission{identities: t.linkedIdentitiesLocked(subject.account)}
	client, known := t.clients[subject.client]
	clientRisky := known && t.highRiskLocked(subject.client, client, now)
	accountRisky := t.accountRiskyLocked(subject.account, now)
	for _, identity := range decision.identities {
		accountRisky = t.accountRiskyLocked(identity, now) || accountRisky
	}

	if !clientRisky && !accountRisky {
		decision.admitted = true
		decision.reservation = t.reserveAccountsLocked(now, subject.account, decision.identities)
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
	decision.reservation = t.reserveAccountsLocked(now, subject.account, decision.identities)
	decision.reservation.client = client
	return decision
}

// admitVerified admits a request that just presented a verified Turnstile
// token and grants the client the bounded allowance: its next clearanceUses
// auth requests within clearanceTTL are admitted without another challenge,
// one at a time, unless a credential failure revokes it first. The returned
// reservation includes the allowance slot this request now occupies.
// identities are the identity keys the spelling linked at admission.
func (t *authChallengeTracker) admitVerified(subject authRiskSubject, identities []string) authReservation {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.clearedUntil = now.Add(t.clearanceTTL)
	client.clearanceLeft = t.clearanceUses
	client.clearanceBusy++

	reservation := t.reserveAccountsLocked(now, subject.account, identities)
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

// recordFailureLinked notes that a credential check failed. It drives the
// client's and the account's repeated-failure signals and revokes the
// client's solved-challenge allowance, so every further guess from a high-risk
// client, or against a high-risk account, needs a fresh challenge.
//
// identity is the identity key the store resolved the account to, or empty
// when the store did not reveal it. The failure then counts against every
// identity the spelling links now and every identity it linked at admission
// (admitted), and the spelling is re-linked to those in case a concurrent
// success dropped the record meanwhile.
func (t *authChallengeTracker) recordFailureLinked(subject authRiskSubject, identity string, admitted []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.failures = appendRecent(client.failures, now, now.Add(-t.failureWindow), t.failureThreshold+1)
	client.revokeClearance()

	if subject.account == "" {
		return
	}
	targets := []string{identity}
	if identity == "" {
		targets = slices.Clone(admitted)
	}
	spelling := t.accountLocked(subject.account, now)
	if spelling != nil {
		spelling.failures = appendRecent(spelling.failures, now, now.Add(-t.failureWindow), t.accountFailureThreshold+1)
		spelling.total++
		if identity == "" {
			spelling.noteAnonymous(subject.exact)
			for _, linked := range spelling.identities {
				if !slices.Contains(targets, linked) {
					targets = append(targets, linked)
				}
			}
		}
	}
	for _, target := range targets {
		if target == "" {
			continue
		}
		// The link is kept even when a saturated table cannot hold the
		// identity record, so admission still treats it as high risk.
		if spelling != nil {
			spelling.link(target)
		}
		account := t.accountLocked(target, now)
		if account == nil {
			continue
		}
		account.failures = appendRecent(account.failures, now, now.Add(-t.failureWindow), t.accountFailureThreshold+1)
		account.total++
		if spelling != nil {
			account.indexSpelling(subject.account)
		}
	}
}

// recordSuccessFor clears failure history after a successful authentication,
// so a legitimate user is not challenged for past typos (NIST SP 800-63B-4
// §3.2.2 resets the retry count of the authenticators used once
// authentication succeeds). It clears the client, the authenticated identity,
// and every indexed spelling that links only that identity, including the one
// used. Its cost is bounded by maxIdentitySpellings, not by the table size.
//
// A spelling that also links another identity, or holds failures that
// revealed no identity and came through a different exact spelling, may be
// shared by distinct store accounts (see forgivableBy); it keeps its failures
// and its other links, so signing in to one account never lowers enforcement
// on another. When identity is empty it is taken from the spelling's single
// linked identity, if it has exactly one. In-flight slots held by concurrent
// requests are kept.
func (t *authChallengeTracker) recordSuccessFor(subject authRiskSubject, identity string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if linked := t.linkedIdentitiesLocked(subject.account); identity == "" && len(linked) == 1 {
		identity = linked[0]
	}

	spellings := []string{subject.account}
	if identity != "" {
		if account, ok := t.accounts[identity]; ok {
			spellings = append(spellings, account.spellings...)
			account.spellings = nil
			account.clearFailures()
			if account.idle() {
				delete(t.accounts, identity)
			}
		}
	}
	for i, key := range spellings {
		account, ok := t.accounts[key]
		if !ok || key == "" || slices.Contains(spellings[:i], key) {
			continue
		}
		// Index entries may be stale: only the spelling used, or one that
		// still links the authenticated identity, is touched.
		if key != subject.account && (identity == "" || !slices.Contains(account.identities, identity)) {
			continue
		}
		account.unlink(identity)
		exact := ""
		if key == subject.account {
			exact = subject.exact
		}
		if !account.forgivableBy(identity, exact) {
			continue
		}
		account.clearFailures()
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

// linkedIdentitiesLocked returns a copy of the identity keys an account
// spelling links. The caller must hold the mutex.
func (t *authChallengeTracker) linkedIdentitiesLocked(spelling string) []string {
	if spelling == "" {
		return nil
	}
	if account, ok := t.accounts[spelling]; ok {
		return slices.Clone(account.identities)
	}
	return nil
}

// reserveAccountsLocked takes an in-flight slot on the spelling record and on
// each linked identity record, creating records as needed. A record the
// saturated table cannot hold is skipped. The caller must hold the mutex.
func (t *authChallengeTracker) reserveAccountsLocked(now time.Time, spelling string, identities []string) authReservation {
	var reservation authReservation
	for _, key := range append([]string{spelling}, identities...) {
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
