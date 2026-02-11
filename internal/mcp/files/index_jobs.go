package files

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// storeCredentialEnvelope encrypts and stores the api key for async workers.
func (s *Service) storeCredentialEnvelope(ctx context.Context, auth AuthContext, project, path string, updatedAt time.Time) error {
	if !s.settings.Search.Enabled {
		return nil
	}
	if s.credential == nil || s.credStore == nil {
		return NewError(ErrCodeSearchBackend, "credential store not configured", false)
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
	key := ref.CacheKey(s.settings.Security.CredentialCachePrefix)
	if err := s.credStore.Store(ctx, key, payload, s.settings.Security.CredentialCacheTTL); err != nil {
		return errors.Wrap(err, "store credential envelope")
	}
	return nil
}
