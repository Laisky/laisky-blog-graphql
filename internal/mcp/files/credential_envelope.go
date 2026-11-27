package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"

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

// credentialKey returns the kv key of the envelope: a digest of the tenant,
// project, path and generation time. The envelope's AAD binds the same fields,
// so an envelope copied under another reference never decrypts.
func (c CredentialReference) credentialKey() string {
	sum := sha256.Sum256(c.AAD())
	return envelopeKeyPrefix + hex.EncodeToString(sum[:])
}

// storeCredentialEnvelopeTx encrypts the caller's key for the index worker and
// reactivates waiting summary refresh jobs, on the caller's transaction: a
// rolled-back mutation leaves neither an envelope nor a reactivated job. With
// search disabled, or without encryption keys, no envelope is written and
// indexing of the path stays lexical.
func (s *Service) storeCredentialEnvelopeTx(ctx context.Context, tx *sql.Tx, auth AuthContext, project, path string, updatedAt time.Time) error {
	if !s.settings.Search.Enabled || s.credential == nil {
		return nil
	}
	ref := CredentialReference{
		APIKeyHash: auth.APIKeyHash,
		Project:    project,
		Path:       path,
		UpdatedAt:  updatedAt,
	}
	payload, err := s.credential.EncryptCredential(ctx, auth.APIKey, ref.AAD())
	if err != nil {
		return errors.Wrap(err, "encrypt credential")
	}
	if err := s.credentials.WithTx(tx).SetWithTTL(ctx, ref.credentialKey(), payload, s.settings.Security.CredentialCacheTTL); err != nil {
		return errors.Wrap(err, "store credential envelope")
	}
	if err := s.reactivateSummaryRefreshJobs(ctx, tx, ref); err != nil {
		return errors.Wrap(err, "reactivate summary refresh jobs")
	}
	return nil
}

// loadCredential decrypts the envelope of a job. Callers degrade indexing on
// any error; credentialUnavailable tells an absent envelope from a failure.
func (s *Service) loadCredential(ctx context.Context, ref CredentialReference) (string, error) {
	if s.credential == nil {
		return "", errors.WithStack(errCredentialEncryptionOff)
	}
	item, err := s.credentials.Get(ctx, ref.credentialKey())
	if err != nil {
		return "", errors.Wrap(err, "load credential envelope")
	}
	apiKey, err := s.credential.DecryptCredential(ctx, item.Value, ref.AAD())
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

// deleteCredential removes the envelope after use. A failure is logged, not
// returned: the envelope still expires with its TTL.
func (s *Service) deleteCredential(ctx context.Context, ref CredentialReference) {
	if err := s.credentials.Del(ctx, ref.credentialKey()); err != nil {
		s.LoggerFromContext(ctx).Warn("delete credential envelope", zap.Error(err))
	}
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
