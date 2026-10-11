package search

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// The former production key is retained only as synthetic regression input.
const configKeyRequireVerified = "settings.mcp.tools.web_fetch.egress.require_verified"

// TestLoadEgressSettingsIgnoresDeprecatedVerificationOverride uses only fresh
// synthetic settings: even an explicit old false cannot disable verification.
func TestLoadEgressSettingsIgnoresDeprecatedVerificationOverride(t *testing.T) {
	original := gconfig.Shared
	gconfig.Shared = gconfig.New()
	t.Cleanup(func() { gconfig.Shared = original })
	gconfig.Shared.Set(configKeyRequireVerified, false)
	require.True(t, LoadEgressSettings().RequireVerified)
}

// TestMandatoryRenderedReceiptIgnoresCompatibilityField proves even direct
// callers with a false legacy field cannot waive missing or empty evidence.
func TestMandatoryRenderedReceiptIgnoresCompatibilityField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*rlibs.HTMLCrawlerTask)
		want   error
	}{
		{"missing", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt = nil }, crawleregress.ErrUnverified},
		{"empty", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt.Origins = nil }, crawleregress.ErrUnverified},
		{"empty forged envelope", func(task *rlibs.HTMLCrawlerTask) {
			task.EgressReceipt.Origins = nil
			task.EgressReceipt.TaskID = "forged"
		}, crawleregress.ErrUnverified},
		{"valid", nil, nil},
		{"private peer", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt.Origins[0].PeerAddress = "127.0.0.1" }, crawleregress.ErrRejected},
		{"mismatched receipt", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt.TaskID = "forged" }, crawleregress.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := rlibs.CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
			task := rlibs.NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/owned-mandatory-fixture", "", true, &policy)
			task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, policy)
			task.EgressReceipt.Origins = []crawleregress.Origin{{URL: task.Url, Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument}}
			if tc.mutate != nil {
				tc.mutate(task)
			}
			err := verifyRenderedTask(context.Background(), EgressSettings{RequireVerified: false}, task.TaskID, task.Url, policy, task)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

// TestFetchConsumerRequiresReceiptWithDeprecatedOverride retains the exact
// shared final consumer's body-withholding decision under synthetic old false.
func TestFetchConsumerRequiresReceiptWithDeprecatedOverride(t *testing.T) {
	original := gconfig.Shared
	gconfig.Shared = gconfig.New()
	t.Cleanup(func() { gconfig.Shared = original })
	gconfig.Shared.Set(configKeyRequireVerified, false)
	for _, tc := range []struct {
		name   string
		mutate func(*rlibs.HTMLCrawlerTask)
		valid  bool
	}{
		{"missing", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt = nil }, false},
		{"empty", func(task *rlibs.HTMLCrawlerTask) { task.EgressReceipt.Origins = nil }, false},
		{"valid", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: tc.mutate}
			body, err := fetchDynamicURLContent(context.Background(), store, "https://1.1.1.1/doc", "", true)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, "safe markdown", string(body))
				return
			}
			require.ErrorIs(t, err, crawleregress.ErrUnverified)
			require.Empty(t, body)
			require.Equal(t, "crawler_egress_unverified", FetchDiagnostic(err).ErrorCode)
		})
	}
}
