package controller

import (
	"crypto/sha256"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
)

// maxClaimedPasskeySessions bounds the memory held for consumed login sessions.
// Sessions expire after passkeySessionTTL, so the registry only needs to cover
// the sessions finished within that window.
const maxClaimedPasskeySessions = 200_000

// passkeySessionClaimGrace extends a claim past its session's expiry.
const passkeySessionClaimGrace = time.Minute

// passkeySessionRegistry remembers which signed passkey login sessions were
// already used until they expire, making each session single use.
//
// The registry is process local. SSO runs as a single instance; a deployment
// with several replicas must route a ceremony's start and finish to the same
// instance or move this registry to shared storage.
type passkeySessionRegistry struct {
	mu      sync.Mutex
	claimed map[[sha256.Size]byte]time.Time
	limit   int
	now     func() time.Time
}

// newPasskeySessionRegistry builds a registry holding at most limit sessions.
func newPasskeySessionRegistry(limit int) *passkeySessionRegistry {
	return &passkeySessionRegistry{
		claimed: make(map[[sha256.Size]byte]time.Time),
		limit:   limit,
		now:     func() time.Time { return gutils.Clock.GetUTCNow() },
	}
}

// claim marks the session identified by challenge as used until expiresAt.
// It returns an error when the session was already claimed, or when the
// registry is full of unexpired sessions; it fails closed rather than evicting
// an entry, because eviction would reopen that session to replay.
func (r *passkeySessionRegistry) claim(challenge string, expiresAt time.Time) error {
	if challenge == "" {
		return errors.New("passkey login session has no challenge")
	}
	key := sha256.Sum256([]byte(challenge))

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if until, ok := r.claimed[key]; ok && now.Before(until) {
		return errors.New("passkey login session was already used")
	}
	if len(r.claimed) >= r.limit {
		for claimedKey, until := range r.claimed {
			if !now.Before(until) {
				delete(r.claimed, claimedKey)
			}
		}
		if len(r.claimed) >= r.limit {
			return errors.New("too many passkey login sessions in flight")
		}
	}
	r.claimed[key] = expiresAt
	return nil
}

var passkeyLoginSessions = newPasskeySessionRegistry(maxClaimedPasskeySessions)

// claimPasskeyLoginSession consumes a verified login session envelope.
// It accepts the envelope and returns an error when it was already used.
func claimPasskeyLoginSession(envelope *passkeySessionEnvelope) error {
	if envelope == nil {
		return errors.New("passkey login session is nil")
	}
	// Keep the claim a little beyond the envelope expiry so a submission in the
	// envelope's final second can never find the claim already pruned.
	return passkeyLoginSessions.claim(envelope.Session.Challenge, time.Unix(envelope.ExpiresAt, 0).UTC().Add(passkeySessionClaimGrace))
}
