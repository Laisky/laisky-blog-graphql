package auth

import (
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// Timing constants of the verdict store.
const (
	// knownKeyRetention keeps a key "known" (eligible for the reserved
	// revalidation budget) long after its trust period, independently of the
	// stale grace window.
	knownKeyRetention = 24 * time.Hour
	// unavailableRetry spaces upstream retries of one known key after one-api
	// failed for it, so an outage cannot spend the reserve on a single key.
	unavailableRetry = 15 * time.Second
	// balanceTTL is how long a balance answer is reused for the same key.
	balanceTTL = 30 * time.Second
)

// positiveEntry records when one-api last accepted a key.
type positiveEntry struct {
	validatedAt time.Time
	// retryAt delays the next upstream call after one-api failed for the key.
	retryAt time.Time
}

// trustLevel is what the verdict store knows about a key at one instant.
type trustLevel int

const (
	// trustUnknown: one-api never accepted the key, or that is forgotten.
	trustUnknown trustLevel = iota
	// trustFresh: one-api accepted the key within the positive TTL.
	trustFresh
	// trustExpired: one-api accepted the key before, but its trust period
	// ended; the key is "known" and revalidates from the reserved budget.
	trustExpired
	// trustRejected: one-api rejected the key within the negative TTL.
	trustRejected
)

// keyTrust is the verdict store's answer for one key.
type keyTrust struct {
	level trustLevel
	// entry is the last acceptance, set for trustFresh and trustExpired.
	entry positiveEntry
}

// known reports whether one-api accepted the key before (fresh or expired).
func (t keyTrust) known() bool {
	return t.level == trustFresh || t.level == trustExpired
}

// verdictStore holds the authority's verdicts by key hash: when one-api last
// accepted a key, which keys it rejected recently, and recent balance
// answers. It decides nothing about upstream calls; it only remembers what
// one-api answered and derives trust from it.
//
// Every update runs under one lock, so an acceptance observed from a consume
// can never undo a concurrently recorded rejection.
type verdictStore struct {
	positiveTTL time.Duration
	negativeTTL time.Duration
	staleGrace  time.Duration
	now         func() time.Time

	mu       sync.Mutex
	positive *ttlCache[positiveEntry]
	// negative maps a rejected key to when the rejection was recorded.
	negative *ttlCache[time.Time]
	balances *ttlCache[Balance]
}

// newVerdictStore builds the store from normalized options.
func newVerdictStore(opts AuthenticatorOptions) *verdictStore {
	return &verdictStore{
		positiveTTL: opts.PositiveTTL,
		negativeTTL: opts.NegativeTTL,
		staleGrace:  opts.StaleGrace,
		now:         opts.Now,
		positive:    newTTLCache[positiveEntry](opts.MaxEntries),
		negative:    newTTLCache[time.Time](opts.MaxEntries),
		balances:    newTTLCache[Balance](opts.MaxEntries),
	}
}

// trust returns what the store knows about key at now.
func (s *verdictStore) trust(key string, now time.Time) keyTrust {
	if entry, ok := s.positive.get(key, now); ok {
		if now.Sub(entry.validatedAt) < s.positiveTTL {
			return keyTrust{level: trustFresh, entry: entry}
		}
		if _, rejected := s.negative.get(key, now); !rejected {
			return keyTrust{level: trustExpired, entry: entry}
		}
	}
	if _, rejected := s.negative.get(key, now); rejected {
		return keyTrust{level: trustRejected}
	}
	return keyTrust{level: trustUnknown}
}

// withinStaleGrace reports whether an acceptance may still be served at now
// while one-api itself gives no verdict.
func (s *verdictStore) withinStaleGrace(entry positiveEntry, now time.Time) bool {
	return s.staleGrace > 0 && now.Sub(entry.validatedAt) < s.positiveTTL+s.staleGrace
}

// recordOutcome is what one recorded upstream verdict changed.
type recordOutcome struct {
	// rejectedKnown: one-api rejected a key it had accepted before
	// (revocation, exhaustion, or one of its own token-store failures).
	rejectedKnown bool
	// superseded: the call accepted the key, but a rejection recorded
	// after the call started is newer evidence, so the acceptance was dropped.
	superseded bool
}

// record stores the outcome err of one upstream call for key that started
// at startedAt. An acceptance is dated at startedAt (one-api accepted the key
// at some instant of the call, so trust never outlasts positive_ttl after
// it), never moves a newer acceptance backwards, and never undoes a
// rejection recorded after the call started: calls for one key can overlap
// (a validation and a balance query), and the newer rejection wins.
// Rejections always win, failing closed.
func (s *verdictStore) record(key string, err error, startedAt time.Time) recordOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	switch {
	case err == nil:
		if rejectedAt, rejected := s.negative.get(key, now); rejected && !rejectedAt.Before(startedAt) {
			return recordOutcome{superseded: true}
		}
		s.negative.remove(key)
		if entry, ok := s.positive.get(key, now); ok && entry.validatedAt.After(startedAt) {
			return recordOutcome{}
		}
		s.positive.put(key, positiveEntry{validatedAt: startedAt}, s.knownUntil(startedAt))
		return recordOutcome{}
	case errors.Is(err, ErrCredentialRejected):
		_, wasKnown := s.positive.get(key, now)
		s.positive.remove(key)
		s.balances.remove(key)
		s.negative.put(key, now, now.Add(s.negativeTTL))
		return recordOutcome{rejectedKnown: wasKnown}
	default:
		if entry, ok := s.positive.get(key, now); ok {
			entry.retryAt = now.Add(unavailableRetry)
			s.positive.put(key, entry, s.knownUntil(entry.validatedAt))
		}
		return recordOutcome{}
	}
}

// refresh moves a known key's last acceptance forward to acceptedAt, the
// start of a call to the authority that accepted the key. It can never
// resurrect a key whose rejection was recorded in the meantime (a rejection
// removes the entry), nor move an acceptance backwards. It reports whether
// the trust period restarted.
func (s *verdictStore) refresh(key string, acceptedAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	entry, known := s.positive.get(key, now)
	if !known || !acceptedAt.After(entry.validatedAt) {
		return false
	}
	if _, rejected := s.negative.get(key, now); rejected {
		return false
	}
	s.positive.put(key, positiveEntry{validatedAt: acceptedAt}, s.knownUntil(acceptedAt))
	return true
}

// knownUntil is when an acceptance at validatedAt stops being "known".
func (s *verdictStore) knownUntil(validatedAt time.Time) time.Time {
	return validatedAt.Add(s.positiveTTL + max(s.staleGrace, knownKeyRetention))
}

// balance returns a recent balance answer for key.
func (s *verdictStore) balance(key string, now time.Time) (Balance, bool) {
	return s.balances.get(key, now)
}

// putBalance keeps a balance answer for key for balanceTTL.
func (s *verdictStore) putBalance(key string, balance Balance) {
	s.balances.put(key, balance, s.now().Add(balanceTTL))
}
