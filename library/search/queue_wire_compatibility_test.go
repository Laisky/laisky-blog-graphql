package search

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// deployed3952CrawlerTask reproduces the exact deployed task JSON schema before receipts.
// VerifyEgressChain has unchanged executable logic between source 3952e43 and PR64;
// this compatibility fixture uses that implementation rather than restating its decisions.
type deployed3952CrawlerPolicy struct {
	Host              string   `json:"host"`
	Addresses         []string `json:"addresses"`
	MaxRedirects      int      `json:"max_redirects"`
	AllowSubresources bool     `json:"allow_subresources"`
}

type deployed3952CrawlerTask struct {
	TaskID         string                     `json:"task_id"`
	CreatedAt      time.Time                  `json:"created_at"`
	Status         string                     `json:"status"`
	FailedReason   *string                    `json:"failed_reason,omitempty"`
	FinishedAt     *time.Time                 `json:"finished_at,omitempty"`
	URL            string                     `json:"url"`
	APIKey         string                     `json:"api_key,omitempty"`
	OutputMarkdown bool                       `json:"output_markdown,omitempty"`
	Egress         *deployed3952CrawlerPolicy `json:"egress,omitempty"`
	RequestChain   []string                   `json:"request_chain,omitempty"`
	ResultHTML     []byte                     `json:"result_html,omitempty"`
	ResultMarkdown []byte                     `json:"result_markdown,omitempty"`
}

// TestNewWorkerResultCompatibleWithDeployed3952 proves worker-first JSON compatibility
// using the deployed decoder shape and actual unchanged legacy URL-chain verifier.
// A passing legacy check is compatibility evidence, not pinned-peer attestation.
func TestNewWorkerResultCompatibleWithDeployed3952(t *testing.T) {
	policy := crawleregress.Policy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1, AllowSubresources: true}
	task := rlibs.NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/initial", "", true, &policy)
	task.Status = rlibs.TaskStatusSuccess
	task.ResultHTML = []byte("<body>verified content</body>")
	task.ResultMarkdown = []byte("verified content")
	task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, policy)
	task.EgressReceipt.Origins = []crawleregress.Origin{
		{URL: task.Url, Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument, BodyBytes: 1},
		{URL: "https://1.1.1.1/final", Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument, BodyBytes: 10},
		{URL: "https://1.1.1.1/style.css", Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginSubresource, BodyBytes: 5},
	}
	// The guarded worker emits only document origins for older server redirect semantics.
	for _, origin := range task.EgressReceipt.Origins {
		if origin.Kind == crawleregress.OriginDocument {
			task.RequestChain = append(task.RequestChain, origin.URL)
		}
	}
	payload, err := task.ToString()
	require.NoError(t, err)
	var deployed deployed3952CrawlerTask
	require.NoError(t, json.Unmarshal([]byte(payload), &deployed))
	require.Equal(t, task.TaskID, deployed.TaskID)
	require.Equal(t, task.Url, deployed.URL)
	require.Equal(t, task.Status, deployed.Status)
	require.Equal(t, task.OutputMarkdown, deployed.OutputMarkdown)
	require.NotNil(t, deployed.Egress)
	require.Equal(t, policy.Host, deployed.Egress.Host)
	require.Equal(t, policy.Addresses, deployed.Egress.Addresses)
	require.Equal(t, policy.MaxRedirects, deployed.Egress.MaxRedirects)
	require.Equal(t, policy.AllowSubresources, deployed.Egress.AllowSubresources)
	require.Equal(t, task.ResultHTML, deployed.ResultHTML)
	require.Equal(t, task.ResultMarkdown, deployed.ResultMarkdown)
	require.Empty(t, deployed.APIKey)
	require.Len(t, deployed.RequestChain, 2)
	require.NoError(t, toolpolicy.VerifyEgressChain(context.Background(), policy, deployed.RequestChain))
	require.NoError(t, crawleregress.VerifyReceipt(context.Background(), task.TaskID, task.Url, policy, task.EgressReceipt))

	// These controls exercise the actual old acceptance boundary without DNS or fetching.
	unfiltered := append(append([]string{}, deployed.RequestChain...), "https://1.1.1.1/style.css")
	require.Error(t, toolpolicy.VerifyEgressChain(context.Background(), policy, unfiltered), "resource entries must not consume legacy redirect budget")
	require.ErrorIs(t, toolpolicy.VerifyEgressChain(context.Background(), policy, nil), toolpolicy.ErrEgressUnverified)
	require.Error(t, toolpolicy.VerifyEgressChain(context.Background(), policy, []string{task.Url, "http://127.0.0.1/private"}))

	// The deployed decoder intentionally ignores additive receipt fields; upgraded
	// verification rejects its reserialized shape rather than accepting URL-only claims.
	oldPayload, err := json.Marshal(deployed)
	require.NoError(t, err)
	upgraded, err := rlibs.NewHTMLCrawlerTaskFromString(string(oldPayload))
	require.NoError(t, err)
	require.Nil(t, upgraded.EgressReceipt)
	require.ErrorIs(t, crawleregress.VerifyReceipt(context.Background(), task.TaskID, task.Url, policy, upgraded.EgressReceipt), crawleregress.ErrUnverified)
}
