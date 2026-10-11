package search

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestLoadEgressSettingsDefaults pins the shipped defaults: a bounded redirect
// budget, no subresource egress, and mandatory renderer evidence unless the
// operator explicitly selects the unsafe compatibility mode.
func TestLoadEgressSettingsDefaults(t *testing.T) {
	original := gconfig.Shared
	gconfig.Shared = gconfig.New()
	t.Cleanup(func() { gconfig.Shared = original })

	settings := LoadEgressSettings()
	require.Equal(t, defaultMaxRedirects, settings.MaxRedirects)
	require.False(t, settings.AllowSubresources,
		"subresource egress must be off unless an operator enables it")
	require.True(t, settings.RequireVerified, "missing configuration must fail closed")

	gconfig.Shared.Set(configKeyMaxRedirects, 2)
	gconfig.Shared.Set(configKeyRequireVerified, true)
	tightened := LoadEgressSettings()
	require.Equal(t, 2, tightened.MaxRedirects)
	require.True(t, tightened.RequireVerified)
	gconfig.Shared.Set(configKeyMaxRedirects, 0)
	require.Zero(t, LoadEgressSettings().MaxRedirects, "explicit zero must forbid redirects")
	gconfig.Shared.Set(configKeyMaxRedirects, -1)
	require.Zero(t, LoadEgressSettings().MaxRedirects)
	gconfig.Shared.Set(configKeyMaxRedirects, 999999)
	require.Equal(t, crawleregress.MaxRedirects, LoadEgressSettings().MaxRedirects)
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

// TestVerifyRenderedEgressFailsClosedOnViolation keeps receipt verification secure even under explicit compatibility opt-out.
func TestVerifyRenderedEgressFailsClosedOnViolation(t *testing.T) {
	task := rlibs.NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/doc", "", true, &rlibs.CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1})
	policy := *task.Egress
	require.ErrorIs(t, verifyRenderedTask(context.Background(), EgressSettings{RequireVerified: true}, task.TaskID, task.Url, policy, task), crawleregress.ErrUnverified)
	require.NoError(t, verifyRenderedTask(context.Background(), EgressSettings{RequireVerified: false}, task.TaskID, task.Url, policy, task))
	task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, policy)
	task.EgressReceipt.Origins = []crawleregress.Origin{{URL: task.Url, Addresses: policy.Addresses, PeerAddress: "127.0.0.1", Kind: crawleregress.OriginDocument}}
	require.ErrorIs(t, verifyRenderedTask(context.Background(), EgressSettings{RequireVerified: false}, task.TaskID, task.Url, policy, task), crawleregress.ErrRejected)
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
