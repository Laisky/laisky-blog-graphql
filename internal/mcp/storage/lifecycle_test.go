package storage

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/stretchr/testify/require"
)

// fakeS3 is a minimal S3 endpoint for one bucket: bucket location, lifecycle
// GET/PUT, and object DELETE. It records what the client sent.
type fakeS3 struct {
	mu        sync.Mutex
	lifecycle []byte // nil means no configuration
	puts      int
	deletes   []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && query.Has("location"):
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case r.Method == http.MethodGet && query.Has("lifecycle"):
		if f.lifecycle == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchLifecycleConfiguration</Code><Message>The lifecycle configuration does not exist</Message></Error>`)
			return
		}
		_, _ = w.Write(f.lifecycle)
	case r.Method == http.MethodPut && query.Has("lifecycle"):
		body, _ := io.ReadAll(r.Body)
		f.lifecycle = body
		f.puts++
	case r.Method == http.MethodDelete:
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/bucket/"))
		f.deletes = append(f.deletes, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// rules decodes the stored lifecycle configuration.
func (f *fakeS3) rules(t *testing.T) []lifecycle.Rule {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := lifecycle.NewConfiguration()
	require.NoError(t, xml.Unmarshal(f.lifecycle, cfg))
	return cfg.Rules
}

func newFakeS3Client(t *testing.T) (*MinIOClient, *fakeS3) {
	t.Helper()
	backend := &fakeS3{}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	client, err := NewMinIOClient(MinIOConfig{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		Bucket:    "bucket",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		UseSSL:    false,
	})
	require.NoError(t, err)
	return client, backend
}

// TestEnsureLifecyclePreservesOtherRules proves EnsureLifecycle only swaps its
// own rule: S3 replaces the whole bucket configuration on PUT, so rules other
// applications installed on a shared bucket must be read back and kept.
func TestEnsureLifecyclePreservesOtherRules(t *testing.T) {
	client, backend := newFakeS3Client(t)
	other := lifecycle.NewConfiguration()
	other.Rules = []lifecycle.Rule{{
		ID:         "other-app-logs",
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "logs/"},
		Expiration: lifecycle.Expiration{Days: 30},
	}}
	seed, err := xml.Marshal(other)
	require.NoError(t, err)
	backend.lifecycle = seed

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, client.EnsureLifecycle(ctx, "mcp/images/", 7))
	// Changing the TTL replaces the previous image rule instead of adding one.
	require.NoError(t, client.EnsureLifecycle(ctx, "mcp/images/", 3))

	rules := backend.rules(t)
	require.Len(t, rules, 2, "the unrelated rule and exactly one image rule")
	require.Equal(t, "other-app-logs", rules[0].ID)
	require.Equal(t, "logs/", rules[0].RuleFilter.Prefix)
	require.Equal(t, "mcp-images-mcp-images--3d", rules[1].ID)
	require.Equal(t, "mcp/images/", rules[1].RuleFilter.Prefix)
	require.EqualValues(t, 3, rules[1].Expiration.Days)
}

// TestEnsureLifecycleWithoutExistingConfiguration installs the rule on a bucket
// that has no lifecycle configuration yet.
func TestEnsureLifecycleWithoutExistingConfiguration(t *testing.T) {
	client, backend := newFakeS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, client.EnsureLifecycle(ctx, "mcp/images/", 7))
	rules := backend.rules(t)
	require.Len(t, rules, 1)
	require.Equal(t, "mcp-images-mcp-images--7d", rules[0].ID)
	require.EqualValues(t, 7, rules[0].Expiration.Days)
}

// TestMinIODeleteIsIdempotent exercises Delete against the S3 wire protocol;
// S3 and MinIO answer 204 whether or not the key exists.
func TestMinIODeleteIsIdempotent(t *testing.T) {
	client, backend := newFakeS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, client.Delete(ctx, "mcp/images/u/abc.png"))
	require.NoError(t, client.Delete(ctx, "mcp/images/u/abc.png"))
	backend.mu.Lock()
	defer backend.mu.Unlock()
	require.Equal(t, []string{"mcp/images/u/abc.png", "mcp/images/u/abc.png"}, backend.deletes)
}

// TestMergeLifecycleRuleKeepsForeignRules covers the merge rules directly.
func TestMergeLifecycleRuleKeepsForeignRules(t *testing.T) {
	existing := lifecycle.NewConfiguration()
	existing.Rules = []lifecycle.Rule{
		{ID: "a", Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: "a/"}},
		{ID: "mcp-images-mcp-images--7d", Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: "mcp/images/"}},
		{ID: "mcp-images-mcp-images2--7d", Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: "mcp/images2/"}},
	}
	merged := mergeLifecycleRule(existing, "mcp/images/", 5)
	ids := make([]string, 0, len(merged.Rules))
	for _, rule := range merged.Rules {
		ids = append(ids, rule.ID)
	}
	require.Equal(t, []string{"a", "mcp-images-mcp-images2--7d", "mcp-images-mcp-images--5d"}, ids)
	require.Len(t, existing.Rules, 3, "the input must not be mutated")

	fresh := mergeLifecycleRule(nil, "p/", 1)
	require.Len(t, fresh.Rules, 1)
}

// multiTransitionLifecycle is a foreign rule minio-go cannot model exactly: it
// keeps only one of the two Transition elements (review finding).
const multiTransitionLifecycle = `<LifecycleConfiguration><Rule><ID>archive</ID><Filter><Prefix>logs/</Prefix></Filter><Status>Enabled</Status>` +
	`<Transition><Days>30</Days><StorageClass>STANDARD_IA</StorageClass></Transition>` +
	`<Transition><Days>90</Days><StorageClass>GLACIER</StorageClass></Transition></Rule></LifecycleConfiguration>`

// TestEnsureLifecycleRefusesLossyRewrite: when a foreign rule would not
// survive the read-modify-write unchanged, EnsureLifecycle must not write at
// all (the caller logs an ERROR asking the operator to add the rule).
func TestEnsureLifecycleRefusesLossyRewrite(t *testing.T) {
	client, backend := newFakeS3Client(t)
	backend.lifecycle = []byte(multiTransitionLifecycle)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLifecycleMergeUnsafe)
	require.Contains(t, err.Error(), "mcp-images-mcp-images--7d", "the error names the rule to add manually")

	backend.mu.Lock()
	defer backend.mu.Unlock()
	require.Zero(t, backend.puts, "nothing may be written")
	require.Equal(t, multiTransitionLifecycle, string(backend.lifecycle))
}

// TestCheckLifecycleRoundTrip covers the structural comparison directly.
func TestCheckLifecycleRoundTrip(t *testing.T) {
	cases := map[string]struct {
		raw  string
		safe bool
	}{
		"simple expiry rule": {`<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>logs</ID>` +
			`<Filter><Prefix>logs/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>30</Days></Expiration></Rule></LifecycleConfiguration>`, true},
		"element order differs": {`<LifecycleConfiguration><Rule><Status>Enabled</Status><Expiration><Days>30</Days></Expiration>` +
			`<ID>logs</ID><Filter><Prefix>logs/</Prefix></Filter></Rule></LifecycleConfiguration>`, true},
		"two transitions": {multiTransitionLifecycle, false},
		"element minio-go does not know": {`<LifecycleConfiguration><Rule><ID>x</ID><Filter><Prefix>a/</Prefix></Filter>` +
			`<Status>Enabled</Status><Expiration><Days>3</Days></Expiration><FutureElement>1</FutureElement></Rule></LifecycleConfiguration>`, false},
		"own rule is ignored": {`<LifecycleConfiguration><Rule><ID>mcp-images-p--7d</ID><Bogus>1</Bogus><Bogus>2</Bogus></Rule></LifecycleConfiguration>`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := lifecycle.NewConfiguration()
			require.NoError(t, xml.Unmarshal([]byte(tc.raw), cfg))
			err := checkLifecycleRoundTrip([]byte(tc.raw), cfg, "mcp-images-p--")
			if tc.safe {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrLifecycleMergeUnsafe)
			}
		})
	}
}
