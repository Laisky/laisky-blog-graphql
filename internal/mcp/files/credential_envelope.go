package files

import (
	"context"
	"database/sql"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/library/db/sql/kv"
)

// envelopeKeyPrefix namespaces FileIO envelopes in the shared kv table.
const envelopeKeyPrefix = "mcp_files_credential_"

// credentialPurgeBatch bounds the rows one expiration sweep deletes.
const credentialPurgeBatch = 500

// errCredentialEncryptionOff reports that no encryption keys are configured,
// so no envelope was ever stored.
var errCredentialEncryptionOff = errors.New("credential encryption is not configured")

// openCredentialEnvelopes returns the envelope store on the kv table that the
// FileIO schema migration creates. It runs no DDL.
func openCredentialEnvelopes(db *sql.DB, clock func() time.Time) (*kv.Kv, error) {
	store, err := kv.Open(db, kv.WithClock(clock))
	if err != nil {
		return nil, errors.Wrap(err, "open credential envelope store")
	}
	return store, nil
}

// credentialKey is the opaque durable envelope identity, not derived from time or scope.
func (c CredentialReference) credentialKey() string {
	return envelopeKeyPrefix + c.ID
}

// storeCredentialEnvelopeTx encrypts the caller's key for the index worker and
// reactivates waiting summary refresh jobs, on the caller's transaction: a
// rolled-back mutation leaves neither an envelope nor a reactivated job. With
// search disabled, or without encryption keys, no envelope is written and
// indexing of the path stays lexical.
func (s *Service) storeCredentialEnvelopeTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path string) (string, error) {
	if !s.settings.Search.Enabled || s.credential == nil {
		return "", nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", errors.Wrap(err, "generate credential ID")
	}
	ref := CredentialReference{
		APIKeyHash: auth.APIKeyHash,
		Project:    project,
		Path:       path,
		ID:         id.String(),
	}
	aad := ref.AAD()
	payload, err := s.credential.EncryptCredential(ctx, auth.APIKey, aad)
	if err != nil {
		return "", errors.Wrap(err, "encrypt credential")
	}
	if err := s.credentials.WithTx(tx).SetWithTTL(ctx, ref.credentialKey(), payload, s.settings.Security.CredentialCacheTTL); err != nil {
		return "", errors.Wrap(err, "store credential envelope")
	}
	if err := s.reactivateSummaryRefreshJobs(ctx, tx, ref); err != nil {
		return "", errors.Wrap(err, "reactivate summary refresh jobs")
	}
	return ref.ID, nil
}

// loadCredential decrypts the envelope of a job. Callers degrade indexing on
// any error; credentialUnavailable tells an absent envelope from a failure.
func (s *Service) loadCredential(ctx context.Context, ref CredentialReference) (string, error) {
	if s.credential == nil {
		return "", errors.WithStack(errCredentialEncryptionOff)
	}
	if ref.ID == "" {
		return "", errors.WithStack(kv.ErrKeyNotFound)
	}
	id, err := uuid.Parse(ref.ID)
	if err != nil || id.Version() != 7 || id.String() != ref.ID {
		return "", errors.New("invalid credential envelope ID")
	}
	item, err := s.credentials.Get(ctx, ref.credentialKey())
	if err != nil {
		return "", errors.Wrap(err, "load credential envelope")
	}
	aad := ref.AAD()
	ciphertext := item.Value
	// The interim PR build prefixed a hex scope marker to the base64 payload.
	// Read those TTL rows without the redundant marker: AES-GCM checks the
	// complete authenticated scope (including the UUID) for both formats.
	if _, payload, ok := strings.Cut(ciphertext, "."); ok {
		ciphertext = payload
	}
	apiKey, err := s.credential.DecryptCredential(ctx, ciphertext, aad)
	if err != nil {
		return "", errors.Wrap(err, "decrypt credential")
	}
	return apiKey, nil
}

// credentialUnavailable reports whether err means there is no usable envelope,
// because it is missing, expired or was never stored, rather than that the
// store failed.
func credentialUnavailable(err error) bool {
	return errors.Is(err, kv.ErrKeyNotFound) || errors.Is(err, kv.ErrKeyExpired) || errors.Is(err, errCredentialEncryptionOff)
}

// purgeExpiredCredentials deletes one bounded batch of expired envelopes. The
// index workers of a service share one sweep per credential TTL; a full batch
// lets the next pass sweep again. A failure is logged and never stops indexing.
func (s *Service) purgeExpiredCredentials(ctx context.Context) {
	now := s.clock().UnixNano()
	next := s.nextCredentialPurge.Load()
	if now < next || !s.nextCredentialPurge.CompareAndSwap(next, now+int64(s.settings.Security.CredentialCacheTTL)) {
		return
	}
	removed, err := s.credentials.DeleteExpired(ctx, credentialPurgeBatch)
	if err != nil {
		s.LoggerFromContext(ctx).Warn("purge expired credential envelopes", zap.Error(err))
		return
	}
	if removed >= credentialPurgeBatch {
		s.nextCredentialPurge.Store(now)
	}
	if removed > 0 {
		s.LoggerFromContext(ctx).Debug("purged expired credential envelopes", zap.Int64("removed", removed))
	}
}
