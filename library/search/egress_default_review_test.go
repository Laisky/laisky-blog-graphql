package search

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestDefaultEgressRejectsMissingRequestChain retains the historic test name while requiring bound connection receipts.
func TestDefaultEgressRejectsMissingRequestChain(t *testing.T) {
	original := gconfig.Shared
	t.Cleanup(func() { gconfig.Shared = original })
	for _, tc := range []struct {
		name         string
		configured   *bool
		wantVerified bool
	}{
		{"unset secure default", nil, true}, {"explicit verification", reviewEgressBoolPointer(true), true}, {"explicit unsafe opt-out", reviewEgressBoolPointer(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gconfig.Shared = gconfig.New()
			if tc.configured != nil {
				gconfig.Shared.Set(configKeyRequireVerified, *tc.configured)
			}
			settings := LoadEgressSettings()
			require.Equal(t, tc.wantVerified, settings.RequireVerified)
			task := rlibs.NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/document", "", true, &rlibs.CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1})
			policy := *task.Egress
			err := verifyRenderedTask(context.Background(), settings, task.TaskID, task.Url, policy, task)
			if tc.wantVerified {
				require.ErrorIs(t, err, crawleregress.ErrUnverified)
			} else {
				require.NoError(t, err)
			}
			task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, policy)
			task.EgressReceipt.Origins = []crawleregress.Origin{{URL: task.Url, Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument}}
			require.NoError(t, verifyRenderedTask(context.Background(), settings, task.TaskID, task.Url, policy, task))
			task.EgressReceipt.Origins = append(task.EgressReceipt.Origins, crawleregress.Origin{URL: "http://127.0.0.1/private", Addresses: []string{"127.0.0.1"}, PeerAddress: "127.0.0.1", Kind: crawleregress.OriginDocument})
			require.ErrorIs(t, verifyRenderedTask(context.Background(), settings, task.TaskID, task.Url, policy, task), crawleregress.ErrRejected)
		})
	}
}
func reviewEgressBoolPointer(value bool) *bool { return &value }
