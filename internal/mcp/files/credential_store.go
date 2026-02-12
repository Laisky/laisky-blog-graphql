package files

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	redislib "github.com/Laisky/go-redis/v2"
)

// RedisCredentialStore stores credential envelopes in Redis.
type RedisCredentialStore struct {
	client *redislib.Utils
}

// NewRedisCredentialStore constructs a redis-backed credential store.
func NewRedisCredentialStore(client *redislib.Utils) *RedisCredentialStore {
	if client == nil {
		return nil
	}
	return &RedisCredentialStore{client: client}
}

// Store writes the credential payload with TTL.
func (s *RedisCredentialStore) Store(ctx context.Context, key, payload string, ttl time.Duration) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is required")
	}
	return s.client.SetItem(ctx, key, payload, ttl)
}

// Load retrieves the credential payload.
func (s *RedisCredentialStore) Load(ctx context.Context, key string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("redis client is required")
	}
	return s.client.GetItem(ctx, key)
}

// Delete removes the cached credential payload.
func (s *RedisCredentialStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil {
		return errors.New("redis client is required")
	}
	return s.client.Del(ctx, key).Err()
}
