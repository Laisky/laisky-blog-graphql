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
	gets      int
	deletes   []string
	// afterLifecyclePut, when set, runs (under mu) after a lifecycle PUT is
	// stored; it models another writer whose PUT lands right after ours.
	afterLifecyclePut func(f *fakeS3)
	// beforeLifecycleGet, when set, runs (without mu) before a lifecycle GET
	// is answered; it lets a test hold readers to force an interleaving.
	beforeLifecycleGet func()
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if hook := f.getHook(); hook != nil && r.Method == http.MethodGet && query.Has("lifecycle") {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && query.Has("location"):
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case r.Method == http.MethodGet && query.Has("lifecycle"):
		f.gets++
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
		if f.afterLifecyclePut != nil {
			f.afterLifecyclePut(f)
		}
	case r.Method == http.MethodDelete:
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/bucket/"))
		f.deletes = append(f.deletes, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// getHook returns beforeLifecycleGet.
func (f *fakeS3) getHook() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.beforeLifecycleGet
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
	require.NoError(t, ensureErr(client.EnsureLifecycle(ctx, "mcp/images/", 7)))
	// Changing the TTL replaces the previous image rule instead of adding one.
	require.NoError(t, ensureErr(client.EnsureLifecycle(ctx, "mcp/images/", 3)))

	rules := backend.rules(t)
	require.Len(t, rules, 2, "the unrelated rule and exactly one image rule")
	require.Equal(t, "other-app-logs", rules[0].ID)
	require.Equal(t, "logs/", rules[0].RuleFilter.Prefix)
	require.Equal(t, lifecycleRuleID("mcp/images/", 3), rules[1].ID)
	require.Equal(t, "mcp/images/", rules[1].RuleFilter.Prefix)
	require.EqualValues(t, 3, rules[1].Expiration.Days)
}

// TestEnsureLifecycleWithoutExistingConfiguration installs the rule on a bucket
// that has no lifecycle configuration yet.
func TestEnsureLifecycleWithoutExistingConfiguration(t *testing.T) {
	client, backend := newFakeS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, ensureErr(client.EnsureLifecycle(ctx, "mcp/images/", 7)))
	rules := backend.rules(t)
	require.Len(t, rules, 1)
	require.Equal(t, lifecycleRuleID("mcp/images/", 7), rules[0].ID)
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
	require.Equal(t, []string{"a", "mcp-images-mcp-images2--7d", lifecycleRuleID("mcp/images/", 5)}, ids)
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
	_, err := client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLifecycleMergeUnsafe)
	require.Contains(t, err.Error(), lifecycleRuleID("mcp/images/", 7), "the error names the rule to add manually")

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
		"own rule is ignored": {`<LifecycleConfiguration><Rule><ID>mcp-images-p--7d</ID><Filter><Prefix>p/</Prefix></Filter>` +
			`<Bogus>1</Bogus><Bogus>2</Bogus></Rule></LifecycleConfiguration>`, true},
		"rule with our ID form but another prefix is compared": {`<LifecycleConfiguration><Rule><ID>mcp-images-p--7d</ID><Filter><Prefix>p:</Prefix></Filter>` +
			`<Bogus>1</Bogus></Rule></LifecycleConfiguration>`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := lifecycle.NewConfiguration()
			require.NoError(t, xml.Unmarshal([]byte(tc.raw), cfg))
			err := checkLifecycleRoundTrip([]byte(tc.raw), cfg, "p/")
			if tc.safe {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrLifecycleMergeUnsafe)
			}
		})
	}
}

// TestMergeLifecycleRuleSeparatesCollidingPrefixes is the review's P2 on
// rule ownership: sanitizeForID maps different characters to the same dash,
// so "mcp/images/" and "mcp:images/" produced the same rule ID prefix and the
// merge replaced the other application's rule on a shared bucket. A rule is
// owned only when its filter prefix is exactly ours, and new rule IDs embed
// a digest of the full prefix so they cannot collide.
func TestMergeLifecycleRuleSeparatesCollidingPrefixes(t *testing.T) {
	require.Equal(t, sanitizeForID("mcp/images/"), sanitizeForID("mcp:images/"), "the fixture must collide")
	foreign := lifecycle.Rule{
		ID:         "mcp-images-mcp-images--7d",
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "mcp:images/"},
		Expiration: lifecycle.Expiration{Days: 7},
	}
	existing := lifecycle.NewConfiguration()
	existing.Rules = []lifecycle.Rule{foreign}

	merged := mergeLifecycleRule(existing, "mcp/images/", 7)
	require.Len(t, merged.Rules, 2, "the colliding foreign rule must be kept")
	require.Equal(t, foreign, merged.Rules[0])
	require.Equal(t, "mcp/images/", merged.Rules[1].RuleFilter.Prefix)
	require.NotEqual(t, foreign.ID, merged.Rules[1].ID, "rule IDs must stay unique within the configuration")
	require.Equal(t, lifecycleRuleID("mcp/images/", 7), merged.Rules[1].ID)
	require.NotEqual(t, lifecycleRuleID("mcp/images/", 7), lifecycleRuleID("mcp:images/", 7))

	// The other deployment's merge keeps ours in turn.
	back := mergeLifecycleRule(merged, "mcp:images/", 7)
	require.Len(t, back.Rules, 2)
	require.Equal(t, merged.Rules[1], back.Rules[0])
	require.Equal(t, "mcp:images/", back.Rules[1].RuleFilter.Prefix)
}

// TestMergeLifecycleRuleReplacesLegacyOwnRule keeps upgrades compatible: a
// rule an earlier release installed under the undigested ID for our exact
// prefix is still ours and is replaced, not duplicated.
func TestMergeLifecycleRuleReplacesLegacyOwnRule(t *testing.T) {
	existing := lifecycle.NewConfiguration()
	existing.Rules = []lifecycle.Rule{{
		ID:         "mcp-images-mcp-images--7d",
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "mcp/images/"},
		Expiration: lifecycle.Expiration{Days: 7},
	}}
	merged := mergeLifecycleRule(existing, "mcp/images/", 7)
	require.Len(t, merged.Rules, 1)
	require.Equal(t, lifecycleRuleID("mcp/images/", 7), merged.Rules[0].ID)
	require.EqualValues(t, 7, merged.Rules[0].Expiration.Days)
}

// TestEnsureLifecycleRefusesLossyRewriteOfCollidingRule: the round-trip
// guard must treat a rule whose ID merely looks like ours, but whose filter
// prefix differs, as foreign, and refuse a rewrite that would alter it.
func TestEnsureLifecycleRefusesLossyRewriteOfCollidingRule(t *testing.T) {
	client, backend := newFakeS3Client(t)
	raw := `<LifecycleConfiguration><Rule><ID>mcp-images-mcp-images--7d</ID><Filter><Prefix>mcp:images/</Prefix></Filter>` +
		`<Status>Enabled</Status><Expiration><Days>7</Days></Expiration><FutureElement>1</FutureElement></Rule></LifecycleConfiguration>`
	backend.lifecycle = []byte(raw)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.ErrorIs(t, err, ErrLifecycleMergeUnsafe)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	require.Zero(t, backend.puts, "the other deployment's rule must not be rewritten")
	require.Equal(t, raw, string(backend.lifecycle))
}

// TestLifecycleRuleIDIsBounded: S3 rule IDs are limited to 255 characters.
func TestLifecycleRuleIDIsBounded(t *testing.T) {
	long := strings.Repeat("deep/", 200)
	id := lifecycleRuleID(long, 36500)
	require.LessOrEqual(t, len(id), 255)
	require.NotEqual(t, id, lifecycleRuleID(long+"x/", 36500))
}

// TestMergeLifecycleRuleNeverDuplicatesIDs (second review): a rule carrying
// our digested ID whose filter was rewritten (for example into an And form)
// was kept as foreign and a second rule with the same ID appended, which S3
// and MinIO reject. A digested ID identifies our rule whatever its filter.
func TestMergeLifecycleRuleNeverDuplicatesIDs(t *testing.T) {
	existing := lifecycle.NewConfiguration()
	existing.Rules = []lifecycle.Rule{{
		ID:         lifecycleRuleID("mcp/images/", 7),
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{And: lifecycle.And{Prefix: "mcp/images/"}},
		Expiration: lifecycle.Expiration{Days: 7},
	}}
	merged := mergeLifecycleRule(existing, "mcp/images/", 7)
	seen := map[string]bool{}
	for _, rule := range merged.Rules {
		require.False(t, seen[rule.ID], "duplicate rule ID %q", rule.ID)
		seen[rule.ID] = true
	}
	require.Len(t, merged.Rules, 1)
	require.Equal(t, "mcp/images/", merged.Rules[0].RuleFilter.Prefix)
}
