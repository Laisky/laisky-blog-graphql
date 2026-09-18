package search

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestLoadEgressSettingsDefaults pins the shipped defaults: a bounded redirect
// budget, no subresource egress, and an unverified crawl that is flagged rather
// than rejected until the operator opts in.
func TestLoadEgressSettingsDefaults(t *testing.T) {
	gconfig.Shared.Set(configKeyMaxRedirects, 0)
	gconfig.Shared.Set(configKeyAllowSubresources, false)
	gconfig.Shared.Set(configKeyRequireVerified, false)
	t.Cleanup(func() {
		gconfig.Shared.Set(configKeyMaxRedirects, 0)
		gconfig.Shared.Set(configKeyAllowSubresources, false)
		gconfig.Shared.Set(configKeyRequireVerified, false)
	})

	settings := LoadEgressSettings()
	require.Equal(t, defaultMaxRedirects, settings.MaxRedirects)
	require.False(t, settings.AllowSubresources,
		"subresource egress must be off unless an operator enables it")
	require.False(t, settings.RequireVerified)

	gconfig.Shared.Set(configKeyMaxRedirects, 2)
	gconfig.Shared.Set(configKeyRequireVerified, true)
	tightened := LoadEgressSettings()
	require.Equal(t, 2, tightened.MaxRedirects)
	require.True(t, tightened.RequireVerified)
}

// TestCrawlerEgressPolicyIsSelfContained checks the envelope handed to the
// renderer. A renderer that honors only these fields must be unable to reach a
// non-public address, and the address order must be stable so a retried task
// serializes identically.
func TestCrawlerEgressPolicyIsSelfContained(t *testing.T) {
	policy := toolpolicy.EgressPolicy{
		Host:         "example.com",
		Addresses:    []string{"93.184.216.34", "1.1.1.1"},
		MaxRedirects: 3,
	}
	envelope := crawlerEgressPolicy(policy)
	require.Equal(t, "example.com", envelope.Host)
	require.Equal(t, []string{"1.1.1.1", "93.184.216.34"}, envelope.Addresses,
		"a stable order keeps a retried task byte-comparable")
	require.Equal(t, 3, envelope.MaxRedirects)
	require.False(t, envelope.AllowSubresources)
}

// TestVerifyRenderedEgressFailsClosedOnViolation is the core G05 guarantee: a
// render result whose reported chain left the admitted origin must never reach
// the caller, even though the original request passed admission.
func TestVerifyRenderedEgressFailsClosedOnViolation(t *testing.T) {
	logger := log.Logger.Named("egress_test")
	policy := toolpolicy.EgressPolicy{Host: "example.com", Addresses: []string{"93.184.216.34"}, MaxRedirects: 1}

	t.Run("a redirect into a private address is rejected", func(t *testing.T) {
		err := verifyRenderedEgress(context.Background(), logger, EgressSettings{MaxRedirects: 1},
			policy, []string{"https://example.com/a", "http://127.0.0.1/admin"})
		require.Error(t, err)
	})

	t.Run("a chain longer than the policy is rejected", func(t *testing.T) {
		err := verifyRenderedEgress(context.Background(), logger, EgressSettings{MaxRedirects: 1},
			policy, []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"})
		require.Error(t, err)
	})

	t.Run("an unverified crawl is flagged by default", func(t *testing.T) {
		require.NoError(t, verifyRenderedEgress(context.Background(), logger,
			EgressSettings{MaxRedirects: 1, RequireVerified: false}, policy, nil))
	})

	t.Run("an unverified crawl is rejected when verification is required", func(t *testing.T) {
		err := verifyRenderedEgress(context.Background(), logger,
			EgressSettings{MaxRedirects: 1, RequireVerified: true}, policy, nil)
		require.Error(t, err)
		require.ErrorIs(t, err, toolpolicy.ErrEgressUnverified)
	})
}

// TestHTMLCrawlerTaskCarriesEgressPolicyThroughSerialization keeps the envelope
// intact across the Redis round trip; a policy dropped in transit silently
// un-pins the crawl.
func TestHTMLCrawlerTaskCarriesEgressPolicyThroughSerialization(t *testing.T) {
	task := rlibs.NewHTMLCrawlerTaskWithEgress("https://example.com/doc", "sk-test", true,
		&rlibs.CrawlerEgressPolicy{Host: "example.com", Addresses: []string{"93.184.216.34"},
			MaxRedirects: 2, AllowSubresources: false})
	task.RequestChain = []string{"https://example.com/doc", "https://example.com/final"}

	encoded, err := task.ToString()
	require.NoError(t, err)
	decoded, err := rlibs.NewHTMLCrawlerTaskFromString(encoded)
	require.NoError(t, err)

	require.NotNil(t, decoded.Egress, "the renderer must receive the connection policy")
	require.Equal(t, "example.com", decoded.Egress.Host)
	require.Equal(t, []string{"93.184.216.34"}, decoded.Egress.Addresses)
	require.Equal(t, 2, decoded.Egress.MaxRedirects)
	require.False(t, decoded.Egress.AllowSubresources)
	require.Equal(t, task.RequestChain, decoded.RequestChain)

	// A task submitted without admission metadata must stay explicitly absent
	// rather than decoding into a permissive zero-valued policy.
	legacy := rlibs.NewHTMLCrawlerTaskWithOptions("https://example.com/doc", "sk-test", true)
	legacyEncoded, err := legacy.ToString()
	require.NoError(t, err)
	legacyDecoded, err := rlibs.NewHTMLCrawlerTaskFromString(legacyEncoded)
	require.NoError(t, err)
	require.Nil(t, legacyDecoded.Egress)
	require.Empty(t, legacyDecoded.RequestChain)
}
