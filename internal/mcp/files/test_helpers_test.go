package files

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// testEmbedder is a stub embedder that returns deterministic vectors.
type testEmbedder struct {
	vector pgvector.Vector
}

// EmbedTexts returns the configured vector for each input.
func (t testEmbedder) EmbedTexts(_ context.Context, _ string, inputs []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, 0, len(inputs))
	for range inputs {
		vectors = append(vectors, t.vector)
	}
	return vectors, nil
}

// errTestEmbedder is a stub embedder that always fails.
type errTestEmbedder struct{}

// EmbedTexts always returns an embedding error for testing degraded indexing.
func (errTestEmbedder) EmbedTexts(context.Context, string, []string) ([]pgvector.Vector, error) {
	return nil, errors.New("embedder unavailable")
}

// memoryCredentialStore keeps credential envelopes in memory.
type memoryCredentialStore struct {
	data map[string]string
}

// Store writes a payload in memory.
func (s *memoryCredentialStore) Store(_ context.Context, key, payload string, _ time.Duration) error {
	if s.data == nil {
		s.data = make(map[string]string)
	}
	s.data[key] = payload
	return nil
}

// Load retrieves a payload from memory.
func (s *memoryCredentialStore) Load(_ context.Context, key string) (string, error) {
	if s.data == nil {
		return "", gorm.ErrRecordNotFound
	}
	value, ok := s.data[key]
	if !ok {
		return "", gorm.ErrRecordNotFound
	}
	return value, nil
}

// Delete removes a payload from memory.
func (s *memoryCredentialStore) Delete(_ context.Context, key string) error {
	if s.data != nil {
		delete(s.data, key)
	}
	return nil
}

// testEncryptionKey returns a stable base64-encoded 32-byte key for tests.
func testEncryptionKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

// newTestDB creates an in-memory sqlite database.
func newTestDB(t *testing.T) *gorm.DB {
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UTC().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	return db
}

// newTestService constructs a service with deterministic dependencies.
func newTestService(t *testing.T, settings Settings, embedder Embedder, store CredentialStore) *Service {
	credential, err := NewCredentialProtector(settings.Security)
	require.NoError(t, err)
	svc, err := NewService(newTestDB(t), settings, embedder, nil, credential, store, nil, nil, func() time.Time {
		return time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	})
	require.NoError(t, err)
	return svc
}
