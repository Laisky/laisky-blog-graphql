package storage

import (
	"context"
	"encoding/xml"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin how EnsureLifecycle behaves next to other
// writers of the same bucket (maintainer review of PR #94, P2). S3 and MinIO
// offer no conditional write for the lifecycle subresource: each writer reads
// the whole configuration, merges its rule and writes the whole configuration
// back, and the last write wins.

// staleWriter returns an afterLifecyclePut hook that, once, replaces the
// configuration with stale plus rule: another deployment that read the
// bucket before our write and writes its own rule right after it.
func staleWriter(t *testing.T, stale []byte, rule lifecycle.Rule) func(*fakeS3) {
	t.Helper()
	var once sync.Once
	return func(f *fakeS3) {
		once.Do(func() {
			cfg := lifecycle.NewConfiguration()
			if stale != nil {
				require.NoError(t, xml.Unmarshal(stale, cfg))
			}
			cfg.Rules = append(cfg.Rules, rule)
			out, err := xml.Marshal(cfg)
			require.NoError(t, err)
			f.lifecycle = out
		})
	}
}

// ruleIDs lists the IDs of the stored rules.
func ruleIDs(t *testing.T, f *fakeS3) []string {
	t.Helper()
	ids := []string{}
	for _, rule := range f.rules(t) {
		ids = append(ids, rule.ID)
	}
	return ids
}

// otherDeploymentRule is the rule of a second deployment on the same bucket.
func otherDeploymentRule() lifecycle.Rule {
	return lifecycle.Rule{
		ID:         lifecycleRuleID("other/images/", 7),
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "other/images/"},
		Expiration: lifecycle.Expiration{Days: 7},
	}
}

// TestEnsureLifecycleDetectsRuleLostToConcurrentWriter reproduces the lost
// update: another deployment read the configuration before our PUT and wrote
// its own rule right after it. EnsureLifecycle used to return success with
// its rule gone; it now reads the configuration back, sees the rule missing
// and merges it again, so both rules survive.
func TestEnsureLifecycleDetectsRuleLostToConcurrentWriter(t *testing.T) {
	client, backend := newFakeS3Client(t)
	backend.afterLifecyclePut = staleWriter(t, nil, otherDeploymentRule())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, ensureErr(client.EnsureLifecycle(ctx, "mcp/images/", 7)))

	require.ElementsMatch(t, []string{lifecycleRuleID("other/images/", 7), lifecycleRuleID("mcp/images/", 7)}, ruleIDs(t, backend),
		"our rule must be reinstalled next to the other deployment's")
}

// TestEnsureLifecycleReportsRuleThatKeepsDisappearing: when every write is
// immediately replaced by a configuration without our rule (a writer that
// owns the whole configuration, such as an IaC apply), EnsureLifecycle gives
// up after a bounded number of attempts with ErrLifecycleRuleMissing instead
// of reporting success.
func TestEnsureLifecycleReportsRuleThatKeepsDisappearing(t *testing.T) {
	client, backend := newFakeS3Client(t)
	foreign := []byte(`<LifecycleConfiguration><Rule><ID>owned-by-iac</ID><Filter><Prefix>logs/</Prefix></Filter>` +
		`<Status>Enabled</Status><Expiration><Days>30</Days></Expiration></Rule></LifecycleConfiguration>`)
	backend.lifecycle = foreign
	backend.afterLifecyclePut = func(f *fakeS3) { f.lifecycle = foreign }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.ErrorIs(t, err, ErrLifecycleRuleMissing)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	require.Equal(t, lifecycleInstallAttempts, backend.puts, "bounded retries")
}

// TestEnsureLifecycleDoesNotRewriteAnUnchangedConfiguration: when the rule is
// already in place, EnsureLifecycle only reads. Every unnecessary
// whole-configuration write is a chance to erase a rule another writer is
// adding at that moment, and periodic checks call it every interval.
func TestEnsureLifecycleDoesNotRewriteAnUnchangedConfiguration(t *testing.T) {
	client, backend := newFakeS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wrote, err := client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.NoError(t, err)
	require.True(t, wrote)
	backend.mu.Lock()
	puts := backend.puts
	backend.mu.Unlock()

	wrote, err = client.EnsureLifecycle(ctx, "mcp/images/", 7)
	require.NoError(t, err)
	require.False(t, wrote, "the rule was in place")
	backend.mu.Lock()
	require.Equal(t, puts, backend.puts, "an unchanged configuration must not be written again")
	backend.mu.Unlock()

	// A changed TTL still replaces the rule.
	require.NoError(t, ensureErr(client.EnsureLifecycle(ctx, "mcp/images/", 3)))
	require.Equal(t, []string{lifecycleRuleID("mcp/images/", 3)}, ruleIDs(t, backend))
}

// TestEnsureLifecycleConcurrentPrefixesConverge runs two deployments that
// install rules for different prefixes at the same moment: both read the
// empty configuration before either writes, so the later PUT erases the
// earlier rule. The read-back after the settle delay sees that and merges
// again, so both rules are in place when the calls return, without any
// periodic check (independent review: an earlier version of this test
// repaired the rule with a second EnsureLifecycle and passed without the
// read-back).
func TestEnsureLifecycleConcurrentPrefixesConverge(t *testing.T) {
	prevSettle := lifecycleSettleDelay
	lifecycleSettleDelay = 100 * time.Millisecond // far longer than the in-memory read-merge-write
	t.Cleanup(func() { lifecycleSettleDelay = prevSettle })
	client, backend := newFakeS3Client(t)
	var arrived sync.WaitGroup
	arrived.Add(2)
	release := make(chan struct{})
	backend.beforeLifecycleGet = func() {
		select {
		case <-release:
			return
		default:
		}
		arrived.Done()
		<-release
	}
	go func() {
		arrived.Wait()
		close(release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prefixes := []string{"a/images/", "b/images/"}
	var wg sync.WaitGroup
	errs := make([]error, len(prefixes))
	wrote := make([]bool, len(prefixes))
	for i, prefix := range prefixes {
		wg.Go(func() { wrote[i], errs[i] = client.EnsureLifecycle(ctx, prefix, 7) })
	}
	wg.Wait()
	for i := range prefixes {
		require.NoError(t, errs[i])
		require.True(t, wrote[i])
	}
	backend.mu.Lock()
	puts := backend.puts
	backend.mu.Unlock()
	require.Equal(t, 3, puts, "both wrote, and the writer whose rule was erased wrote again")
	require.ElementsMatch(t, []string{lifecycleRuleID("a/images/", 7), lifecycleRuleID("b/images/", 7)}, ruleIDs(t, backend))
	for _, prefix := range prefixes {
		require.NoError(t, client.CheckLifecycle(ctx, prefix, 7), "rule for %q", prefix)
	}
}

// TestCheckLifecycleIsReadOnly covers the verify mode used when the rule is
// managed outside the service (IaC): it never writes, accepts any enabled
// rule that expires every object under the prefix within the TTL, and
// reports anything else as ErrLifecycleRuleMissing.
func TestCheckLifecycleIsReadOnly(t *testing.T) {
	cases := map[string]struct {
		raw     string
		covered bool
	}{
		"no configuration": {"", false},
		"own rule":         {ownRuleXML("mcp/images/", 7, 7, "Enabled"), true},
		"broader iac rule": {`<LifecycleConfiguration><Rule><ID>iac</ID><Filter><Prefix>mcp/</Prefix></Filter>` +
			`<Status>Enabled</Status><Expiration><Days>3</Days></Expiration></Rule></LifecycleConfiguration>`, true},
		"longer ttl":      {ownRuleXML("mcp/images/", 7, 30, "Enabled"), false},
		"disabled":        {ownRuleXML("mcp/images/", 7, 7, "Disabled"), false},
		"other prefix":    {ownRuleXML("mcp/other/", 7, 7, "Enabled"), false},
		"narrower prefix": {ownRuleXML("mcp/images/u/", 7, 7, "Enabled"), false},
		"tag filtered": {`<LifecycleConfiguration><Rule><ID>tagged</ID><Filter><And><Prefix>mcp/images/</Prefix>` +
			`<Tag><Key>k</Key><Value>v</Value></Tag></And></Filter><Status>Enabled</Status><Expiration><Days>7</Days></Expiration></Rule></LifecycleConfiguration>`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client, backend := newFakeS3Client(t)
			if tc.raw != "" {
				backend.lifecycle = []byte(tc.raw)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := client.CheckLifecycle(ctx, "mcp/images/", 7)
			if tc.covered {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrLifecycleRuleMissing)
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			require.Zero(t, backend.puts)
		})
	}
}

// ownRuleXML renders a rule with this package's ID for prefix and idDays,
// expiring after days.
func ownRuleXML(prefix string, idDays, days int, status string) string {
	cfg := lifecycle.NewConfiguration()
	cfg.Rules = []lifecycle.Rule{{
		ID:         lifecycleRuleID(prefix, idDays),
		Status:     status,
		RuleFilter: lifecycle.Filter{Prefix: prefix},
		Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(days)},
	}}
	out, _ := xml.Marshal(cfg)
	return string(out)
}
