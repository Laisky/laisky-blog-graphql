package controller

import (
	"cmp"
	"slices"
	"time"
)

// saturationRecheckInterval rate-limits the sweep that decides whether a
// saturated account table has room again.
const saturationRecheckInterval = time.Second

// clientLocked returns the risk record for a client, creating it if absent and
// keeping the table within maxClients. Only clients with a request in flight
// on their allowance are never evicted, so the table can exceed its bound by
// at most the number of such concurrent requests. The caller must hold the
// mutex.
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

// accountLocked returns the risk record for an account key, creating it if
// absent. When the table is full and nothing may be evicted it marks the table
// saturated and returns nil: the account is then untracked and treated as high
// risk (see accountRiskyLocked) rather than evicting an account under attack.
// The caller must hold the mutex.
func (t *authChallengeTracker) accountLocked(key string, now time.Time) *accountAuthRisk {
	if account, ok := t.accounts[key]; ok {
		return account
	}

	if len(t.accounts) >= t.maxAccounts {
		t.shrinkAccountsLocked(now)
		if len(t.accounts) >= t.maxAccounts {
			t.accountsSaturated = true
			t.saturationCheckedAt = now
			return nil
		}
	}
	account := &accountAuthRisk{}
	t.accounts[key] = account
	return account
}

// accountsSaturatedLocked reports whether the account table is saturated,
// re-checking for room at most once per saturationRecheckInterval. The caller
// must hold the mutex.
func (t *authChallengeTracker) accountsSaturatedLocked(now time.Time) bool {
	if !t.accountsSaturated {
		return false
	}
	if now.Sub(t.saturationCheckedAt) < saturationRecheckInterval {
		return true
	}

	t.saturationCheckedAt = now
	t.shrinkAccountsLocked(now)
	t.accountsSaturated = len(t.accounts) >= t.maxAccounts
	return t.accountsSaturated
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
// addresses instead of a client close to its challenge threshold. Clients with
// a request in flight on their allowance are kept. The caller must hold the
// mutex.
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
		if client.idle(now) {
			delete(t.clients, key)
			continue
		}
		if client.clearanceBusy == 0 {
			candidates = append(candidates, candidate{key: key, client: client})
		}
	}
	excess := len(t.clients) - target
	if excess <= 0 {
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
	for _, c := range candidates[:min(excess, len(candidates))] {
		delete(t.clients, c.key)
	}
}

// shrinkAccountsLocked drops expired account records and, if the table is
// still above its shrink target, evicts accounts below their failure
// threshold: fewest recent failures, then fewest failures overall, then the
// longest idle. Accounts at or above the threshold and accounts with requests
// in flight are never evicted, so a flood of failures against unique (for
// example non-existent) accounts cannot push out an account under attack; once
// only such accounts remain the table saturates instead. The caller must hold
// the mutex.
func (t *authChallengeTracker) shrinkAccountsLocked(now time.Time) {
	target := shrinkTarget(t.maxAccounts)
	type candidate struct {
		key     string
		account *accountAuthRisk
	}
	candidates := make([]candidate, 0, len(t.accounts))
	for key, account := range t.accounts {
		account.failures = keepRecent(account.failures, now.Add(-t.failureWindow))
		if account.idle() {
			delete(t.accounts, key)
			continue
		}
		if account.inflight == 0 && len(account.failures) < t.accountFailureThreshold {
			candidates = append(candidates, candidate{key: key, account: account})
		}
	}
	excess := len(t.accounts) - target
	if excess <= 0 {
		return
	}

	slices.SortFunc(candidates, func(a, b candidate) int {
		return cmp.Or(
			cmp.Compare(len(a.account.failures), len(b.account.failures)),
			cmp.Compare(a.account.total, b.account.total),
			a.account.failures[len(a.account.failures)-1].Compare(b.account.failures[len(b.account.failures)-1]),
		)
	})
	for _, c := range candidates[:min(excess, len(candidates))] {
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
