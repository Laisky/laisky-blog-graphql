package controller

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync"
	"time"

	gconfig "github.com/Laisky/go-config/v2"
	gutils "github.com/Laisky/go-utils/v6"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
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

	// accountKeyBytes is how much of the account HMAC is kept: 128 bits make a
	// collision between two tracked accounts negligible.
	accountKeyBytes = 16
	// maxAccountKeyInput bounds the account bytes hashed per request. The gate
	// runs before the resolvers' length checks, and every login store rejects
	// accounts far shorter than this, so longer inputs only collide with other
	// invalid accounts.
	maxAccountKeyInput = 1024
)

// authRiskSubject names who an authentication request is attributed to: the
// network client sending it and, for flows that name an account, the account
// it targets. A client rotating addresses gets a fresh client record, but every
// guess against one account lands on the same account record.
type authRiskSubject struct {
	// client is the network identity key (see resolveAuthClientKey).
	client string
	// account is the keyed hash of the normalized account, or empty when the
	// flow names no account (GitHub OAuth, passkey and registration flows).
	account string
}

// authChallengeTracker records recent authentication attempts so the SSO flow
// can demand a Turnstile challenge only for suspicious requests: clients making
// frequent requests or accumulating failures, and accounts accumulating
// failures from any client. Low-risk requests authenticate without a challenge.
// It only ever escalates to a challenge; it never locks an account out.
//
// Both tables are bounded: records expire with their signal windows and, when a
// table is full, the records carrying the least evidence are evicted first.
// Accounts are held only as keyed hashes.
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

// accountAuthRisk holds the recent credential failures against one account,
// from any client.
type accountAuthRisk struct {
	failures []time.Time
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
// raw account the request names, which may be empty. The account is normalized
// exactly as the login lookup normalizes it, so case or whitespace variants of
// one account share a record.
func (t *authChallengeTracker) subject(clientKey string, account string) authRiskSubject {
	return authRiskSubject{client: clientKey, account: t.accountKey(account)}
}

// accountKey returns the keyed hash of the normalized account, or "" when the
// account is blank.
func (t *authChallengeTracker) accountKey(account string) string {
	if len(account) > maxAccountKeyInput {
		account = account[:maxAccountKeyInput]
	}
	normalized := service.NormalizeLoginAccount(account)
	if normalized == "" {
		return ""
	}

	mac := hmac.New(sha256.New, t.accountHashKey)
	_, _ = mac.Write([]byte(normalized)) // hash.Hash.Write never returns an error
	return hex.EncodeToString(mac.Sum(nil)[:accountKeyBytes])
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

// recordFailure notes that a credential check failed. It drives the client's
// and the account's repeated-failure signals and revokes the client's
// solved-challenge allowance, so every further guess from a high-risk client,
// or against a high-risk account, needs a fresh challenge.
func (t *authChallengeTracker) recordFailure(subject authRiskSubject) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.failures = appendRecent(client.failures, now, now.Add(-t.failureWindow), t.failureThreshold+1)
	client.revokeClearance()

	if subject.account == "" {
		return
	}
	account := t.accountLocked(subject.account, now)
	account.failures = appendRecent(account.failures, now, now.Add(-t.failureWindow), t.accountFailureThreshold+1)
}

// recordChallengePassed grants the allowance earned by a verified Turnstile
// token: the client's next clearanceUses auth requests within clearanceTTL are
// admitted without another challenge unless a credential failure revokes it.
func (t *authChallengeTracker) recordChallengePassed(subject authRiskSubject) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client := t.clientLocked(subject.client, now)
	client.clearedUntil = now.Add(t.clearanceTTL)
	client.clearanceLeft = t.clearanceUses
}

// admitWithoutChallenge decides whether the current auth request may proceed
// without a Turnstile token. A request whose client and account are both low
// risk always may. Otherwise it may only by spending one unit of the client's
// solved-challenge allowance.
func (t *authChallengeTracker) admitWithoutChallenge(subject authRiskSubject) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	client, known := t.clients[subject.client]
	clientRisky := known && t.highRiskLocked(subject.client, client, now)
	if !clientRisky && !t.accountHighRiskLocked(subject.account, now) {
		return true
	}

	// highRiskLocked may have dropped an idle record; re-read it.
	client, known = t.clients[subject.client]
	if !known || !client.hasClearance(now) {
		return false
	}
	client.clearanceLeft--
	return true
}

// recordSuccess clears the failure history of the client and of the account
// after a successful authentication, so a legitimate user is not challenged
// for past typos (NIST SP 800-63B-4 §3.2.2 resets the retry count of the
// authenticators used once authentication succeeds).
func (t *authChallengeTracker) recordSuccess(subject authRiskSubject) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if subject.account != "" {
		delete(t.accounts, subject.account)
	}

	client, ok := t.clients[subject.client]
	if !ok {
		return
	}
	client.failures = nil
	if len(client.attempts) == 0 {
		delete(t.clients, subject.client)
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

// accountHighRiskLocked prunes expired failures and reports whether the
// account has reached its failure threshold. The caller must hold the mutex.
func (t *authChallengeTracker) accountHighRiskLocked(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	account, ok := t.accounts[key]
	if !ok {
		return false
	}

	account.failures = keepRecent(account.failures, now.Add(-t.failureWindow))
	if len(account.failures) == 0 {
		delete(t.accounts, key)
		return false
	}

	return len(account.failures) >= t.accountFailureThreshold
}

// clientLocked returns the risk record for a client, creating it if absent and
// keeping the table within maxClients. The caller must hold the mutex.
func (t *authChallengeTracker) clientLocked(key string, now time.Time) *clientAuthRisk {
	if client, ok := t.clients[key]; ok {
		return client
	}

	if len(t.clients) >= t.maxClients {
		t.shrinkClientsLocked(now)
	}
	client := &clientAuthRisk{}
	t.clients[key] = client
	return client
}

// accountLocked returns the risk record for an account, creating it if absent
// and keeping the table within maxAccounts. The caller must hold the mutex.
func (t *authChallengeTracker) accountLocked(key string, now time.Time) *accountAuthRisk {
	if account, ok := t.accounts[key]; ok {
		return account
	}

	if len(t.accounts) >= t.maxAccounts {
		t.shrinkAccountsLocked(now)
	}
	account := &accountAuthRisk{}
	t.accounts[key] = account
	return account
}

// shrinkTarget is the size a full table is cut down to: an eighth below its
// bound, so the sweep and sort that make room run once per many inserts.
func shrinkTarget(limit int) int {
	return limit - max(1, limit/8)
}

// shrinkClientsLocked drops expired client records and, if the table is still
// above its shrink target, evicts the records carrying the least evidence of
// abuse: fewest failures, then no allowance, then fewest attempts, then the
// longest idle. A flood of one-off addresses therefore displaces other one-off
// addresses instead of a client close to its challenge threshold. The caller
// must hold the mutex.
func (t *authChallengeTracker) shrinkClientsLocked(now time.Time) {
	target := shrinkTarget(t.maxClients)
	type candidate struct {
		key    string
		client *clientAuthRisk
	}
	candidates := make([]candidate, 0, len(t.clients))
	for key, client := range t.clients {
		client.attempts = keepRecent(client.attempts, now.Add(-t.frequencyWindow))
		client.failures = keepRecent(client.failures, now.Add(-t.failureWindow))
		if len(client.attempts) == 0 && len(client.failures) == 0 && !client.hasClearance(now) {
			delete(t.clients, key)
			continue
		}
		candidates = append(candidates, candidate{key: key, client: client})
	}
	if len(t.clients) <= target {
		return
	}

	slices.SortFunc(candidates, func(a, b candidate) int {
		return cmp.Or(
			cmp.Compare(len(a.client.failures), len(b.client.failures)),
			compareBool(a.client.hasClearance(now), b.client.hasClearance(now)),
			cmp.Compare(len(a.client.attempts), len(b.client.attempts)),
			a.client.lastSeen().Compare(b.client.lastSeen()),
		)
	})
	for _, c := range candidates[:len(t.clients)-target] {
		delete(t.clients, c.key)
	}
}

// shrinkAccountsLocked drops expired account records and, if the table is
// still above its shrink target, evicts the accounts with the fewest recent
// failures, oldest first. A flood of failures against unique (for example
// non-existent) accounts therefore evicts other single-failure records, not an
// account under sustained attack. The caller must hold the mutex.
func (t *authChallengeTracker) shrinkAccountsLocked(now time.Time) {
	target := shrinkTarget(t.maxAccounts)
	type candidate struct {
		key     string
		account *accountAuthRisk
	}
	candidates := make([]candidate, 0, len(t.accounts))
	for key, account := range t.accounts {
		account.failures = keepRecent(account.failures, now.Add(-t.failureWindow))
		if len(account.failures) == 0 {
			delete(t.accounts, key)
			continue
		}
		candidates = append(candidates, candidate{key: key, account: account})
	}
	if len(t.accounts) <= target {
		return
	}

	slices.SortFunc(candidates, func(a, b candidate) int {
		return cmp.Or(
			cmp.Compare(len(a.account.failures), len(b.account.failures)),
			a.account.failures[len(a.account.failures)-1].Compare(b.account.failures[len(b.account.failures)-1]),
		)
	})
	for _, c := range candidates[:len(t.accounts)-target] {
		delete(t.accounts, c.key)
	}
}

// compareBool orders false before true.
func compareBool(a bool, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
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
		if threshold := gconfig.Shared.GetInt(base + ".account_failure_threshold"); threshold > 0 {
			authChallengeInst.accountFailureThreshold = threshold
		}
		if limit := gconfig.Shared.GetInt(base + ".max_tracked_accounts"); limit > 0 {
			authChallengeInst.maxAccounts = limit
		}
		if seconds := gconfig.Shared.GetInt(base + ".clearance_seconds"); seconds > 0 {
			authChallengeInst.clearanceTTL = time.Duration(seconds) * time.Second
		}
		if uses := gconfig.Shared.GetInt(base + ".clearance_uses"); uses > 0 {
			authChallengeInst.clearanceUses = uses
		}
	}
	return authChallengeInst
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
