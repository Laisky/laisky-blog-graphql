package storage

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"github.com/stretchr/testify/require"
)

// MinIO acceptance environment. The test needs a disposable bucket on a
// disposable MinIO (or S3-compatible) server: it replaces the bucket's whole
// lifecycle configuration. It is deliberate acceptance, not fast CI:
//
//	MCP_STORAGE_TEST_MINIO_ENDPOINT=127.0.0.1:19000 \
//	MCP_STORAGE_TEST_MINIO_BUCKET=imgaccept \
//	MCP_STORAGE_TEST_MINIO_ACCESS_KEY=... MCP_STORAGE_TEST_MINIO_SECRET_KEY=... \
//	go test -count=1 ./internal/mcp/storage -run TestMinIOLifecycleAcceptance
const (
	minioAcceptanceEndpointEnv = "MCP_STORAGE_TEST_MINIO_ENDPOINT"
	minioAcceptanceBucketEnv   = "MCP_STORAGE_TEST_MINIO_BUCKET"
	minioAcceptanceAccessEnv   = "MCP_STORAGE_TEST_MINIO_ACCESS_KEY"
	minioAcceptanceSecretEnv   = "MCP_STORAGE_TEST_MINIO_SECRET_KEY"
)

// productionLifecycleSettleDelay is lifecycleSettleDelay's shipped value.
const productionLifecycleSettleDelay = 3 * time.Second

// acceptanceClient returns a client for the acceptance server, or skips.
func acceptanceClient(t *testing.T) *MinIOClient {
	t.Helper()
	endpoint := os.Getenv(minioAcceptanceEndpointEnv)
	if endpoint == "" {
		t.Skipf("%s not set", minioAcceptanceEndpointEnv)
	}
	client, err := NewMinIOClient(MinIOConfig{
		Endpoint:  endpoint,
		Bucket:    os.Getenv(minioAcceptanceBucketEnv),
		AccessKey: os.Getenv(minioAcceptanceAccessEnv),
		SecretKey: os.Getenv(minioAcceptanceSecretEnv),
	})
	require.NoError(t, err)
	return client
}

// TestMinIOLifecycleAcceptance runs the lifecycle protocol against a real
// server: the rule this package writes must be recognized when the server
// echoes it back (otherwise every check would rewrite the configuration and
// the read-back would report it missing), foreign rules must survive, and
// concurrent installs for different prefixes must converge after one
// periodic check per deployment.
func TestMinIOLifecycleAcceptance(t *testing.T) {
	client := acceptanceClient(t)
	prevSettle := lifecycleSettleDelay
	lifecycleSettleDelay = productionLifecycleSettleDelay
	t.Cleanup(func() { lifecycleSettleDelay = prevSettle })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	raw, err := client.ensureClient()
	require.NoError(t, err)
	require.NoError(t, client.VerifyBucket(ctx))

	foreign := lifecycle.NewConfiguration()
	foreign.Rules = []lifecycle.Rule{{
		ID:         "acceptance-foreign-logs",
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: "logs/"},
		Expiration: lifecycle.Expiration{Days: 30},
	}}
	require.NoError(t, raw.SetBucketLifecycle(ctx, client.cfg.Bucket, foreign))
	t.Cleanup(func() {
		_ = raw.SetBucketLifecycle(context.Background(), client.cfg.Bucket, lifecycle.NewConfiguration())
	})

	require.NoError(t, client.EnsureLifecycle(ctx, "mcp/images/", 7))
	echoed, err := client.bucketLifecycle(ctx, raw)
	require.NoError(t, err)
	require.True(t, hasOwnLifecycleRule(echoed, "mcp/images/", 7), "the server's echo of our rule must be recognized: %+v", echoed.Rules)
	require.NoError(t, client.CheckLifecycle(ctx, "mcp/images/", 7))
	require.ErrorIs(t, client.CheckLifecycle(ctx, "elsewhere/", 7), ErrLifecycleRuleMissing)

	before, err := client.rawBucketLifecycle(ctx, raw)
	require.NoError(t, err)
	require.NoError(t, client.EnsureLifecycle(ctx, "mcp/images/", 7))
	after, err := client.rawBucketLifecycle(ctx, raw)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "an unchanged configuration is not rewritten")

	// Concurrent installs of several deployments, then one periodic check each.
	prefixes := []string{"dep-a/images/", "dep-b/images/", "dep-c/images/", "dep-d/images/"}
	for round := range 5 {
		var wg sync.WaitGroup
		for _, prefix := range prefixes {
			wg.Go(func() {
				// ErrLifecycleRuleMissing is acceptable here: the periodic check repairs it.
				if err := client.EnsureLifecycle(ctx, prefix, 7+round); err != nil {
					require.ErrorIs(t, err, ErrLifecycleRuleMissing)
				}
			})
		}
		wg.Wait()
		// The periodic check of manage mode: EnsureLifecycle again, which
		// only reads when the rule is in place.
		for _, prefix := range prefixes {
			require.NoError(t, client.EnsureLifecycle(ctx, prefix, 7+round))
		}
		current, err := client.bucketLifecycle(ctx, raw)
		require.NoError(t, err)
		for _, prefix := range append([]string{"mcp/images/"}, prefixes...) {
			days := 7 + round
			if prefix == "mcp/images/" {
				days = 7
			}
			require.True(t, hasOwnLifecycleRule(current, prefix, days), "round %d: rule for %q", round, prefix)
		}
		require.True(t, ruleWithID(current, "acceptance-foreign-logs"), "round %d: the foreign rule must survive", round)
		require.Len(t, current.Rules, 1+1+len(prefixes), fmt.Sprintf("round %d: no duplicate or stale rules", round))
	}
}

// ruleWithID reports whether cfg has a rule with id.
func ruleWithID(cfg *lifecycle.Configuration, id string) bool {
	for _, rule := range cfg.Rules {
		if rule.ID == id {
			return true
		}
	}
	return false
}
