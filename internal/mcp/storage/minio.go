package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

// MinIOConfig holds the connection parameters for the MinIO object store.
type MinIOConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// MinIOClient implements ObjectStore against a real MinIO / S3 endpoint.
// The client wraps a lazily-initialized underlying *minio.Client using an
// RWMutex (not sync.Once) so that a transient init failure can be retried.
type MinIOClient struct {
	cfg MinIOConfig

	mu     sync.RWMutex
	client *minio.Client
}

// NewMinIOClient constructs a MinIOClient from the provided configuration.
// The underlying *minio.Client is created lazily on first use.
func NewMinIOClient(cfg MinIOConfig) (*MinIOClient, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("storage: minio endpoint is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("storage: minio bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("storage: minio credentials are required")
	}
	return &MinIOClient{cfg: cfg}, nil
}

// ensureClient returns a cached *minio.Client, constructing one on first call
// and retrying after transient failures.
func (m *MinIOClient) ensureClient() (*minio.Client, error) {
	m.mu.RLock()
	if m.client != nil {
		c := m.client
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return m.client, nil
	}

	client, err := minio.New(m.cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(m.cfg.AccessKey, m.cfg.SecretKey, ""),
		Secure: m.cfg.UseSSL,
	})
	if err != nil {
		return nil, errors.Wrap(err, "storage: construct minio client")
	}
	m.client = client
	return client, nil
}

// VerifyBucket probes the configured bucket to confirm the endpoint is reachable
// and the credentials grant access. Intended for startup validation so that a
// misconfigured MinIO fails fast instead of surfacing on the first upload.
func (m *MinIOClient) VerifyBucket(ctx context.Context) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: verify bucket ensure client")
	}
	exists, err := client.BucketExists(ctx, m.cfg.Bucket)
	if err != nil {
		return errors.Wrapf(err, "storage: probe bucket %q", m.cfg.Bucket)
	}
	if !exists {
		return errors.Errorf("storage: bucket %q not found", m.cfg.Bucket)
	}
	return nil
}

// Put uploads an object; identical keys are overwritten deterministically.
func (m *MinIOClient) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string, userMeta map[string]string) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: put ensure client")
	}
	opts := minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: userMeta,
	}
	if _, err := client.PutObject(ctx, m.cfg.Bucket, key, body, size, opts); err != nil {
		return errors.Wrapf(err, "storage: put object %q", key)
	}
	return nil
}

// Get fetches an object and returns a ReadCloser that the caller must close.
// The object's size is also returned for efficient buffering.
func (m *MinIOClient) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	client, err := m.ensureClient()
	if err != nil {
		return nil, 0, errors.Wrap(err, "storage: get ensure client")
	}
	obj, err := client.GetObject(ctx, m.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, errors.Wrapf(err, "storage: get object %q", key)
	}
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, 0, errors.Wrapf(err, "storage: stat object %q", key)
	}
	return obj, stat.Size, nil
}

// PresignedGet returns a short-lived URL pointing at the object.
func (m *MinIOClient) PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	client, err := m.ensureClient()
	if err != nil {
		return "", errors.Wrap(err, "storage: presign ensure client")
	}
	u, err := client.PresignedGetObject(ctx, m.cfg.Bucket, key, ttl, nil)
	if err != nil {
		return "", errors.Wrapf(err, "storage: presign object %q", key)
	}
	return u.String(), nil
}

// EnsureLifecycle installs an expiry rule restricted to the provided prefix.
// The rule is keyed by a deterministic ID so repeated calls replace, not
// duplicate, the previous rule.
func (m *MinIOClient) EnsureLifecycle(ctx context.Context, prefix string, days int) error {
	client, err := m.ensureClient()
	if err != nil {
		return errors.Wrap(err, "storage: lifecycle ensure client")
	}
	if days <= 0 {
		return errors.Errorf("storage: lifecycle days must be positive, got %d", days)
	}
	ruleID := fmt.Sprintf("mcp-images-%s-%dd", sanitizeForID(prefix), days)
	rule := lifecycle.Rule{
		ID:     ruleID,
		Status: "Enabled",
		RuleFilter: lifecycle.Filter{
			Prefix: prefix,
		},
		Expiration: lifecycle.Expiration{
			Days: lifecycle.ExpirationDays(days),
		},
	}
	cfg := &lifecycle.Configuration{Rules: []lifecycle.Rule{rule}}
	if err := client.SetBucketLifecycle(ctx, m.cfg.Bucket, cfg); err != nil {
		return errors.Wrap(err, "storage: set bucket lifecycle")
	}
	return nil
}

// sanitizeForID strips characters that are not safe inside a lifecycle rule ID.
func sanitizeForID(in string) string {
	b := strings.Builder{}
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
