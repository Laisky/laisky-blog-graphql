package redis

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// TestCrawlerProducerAdvertisesProviderFallback verifies that the actual queue
// envelope opts into provider results without adding caller credentials.
func TestCrawlerProducerAdvertisesProviderFallback(t *testing.T) {
	server := miniredis.RunT(t)
	db := NewDB(&redis.Options{Addr: server.Addr(), Protocol: 2, MaxRetries: -1, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
	id, err := db.AddHTMLCrawlerTaskWithOptions(t.Context(), "https://1.1.1.1/article", "synthetic-caller-key", true)
	require.NoError(t, err)
	pending, err := server.List(KeyTaskHTMLCrawlerPending)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(pending[0]), &envelope))
	require.Equal(t, id, envelope["task_id"])
	require.Equal(t, true, envelope["allow_provider_fallback"])
	require.NotContains(t, envelope, "api_key")
	require.NotContains(t, envelope, "acquisition", "only the worker reports the actual acquisition method")
}

// receiptOnlyCrawlerTask is the relevant deployed verified-v1 wire shape before
// provider negotiation. Its decoder deliberately cannot retain the new fields.
type receiptOnlyCrawlerTask struct {
	TaskID         string                 `json:"task_id"`
	CreatedAt      time.Time              `json:"created_at"`
	Status         string                 `json:"status"`
	URL            string                 `json:"url"`
	OutputMarkdown bool                   `json:"output_markdown,omitempty"`
	Egress         *CrawlerEgressPolicy   `json:"egress,omitempty"`
	RequestChain   []string               `json:"request_chain,omitempty"`
	EgressReceipt  *crawleregress.Receipt `json:"egress_receipt,omitempty"`
	ResultHTML     []byte                 `json:"result_html,omitempty"`
	ResultMarkdown []byte                 `json:"result_markdown,omitempty"`
}

// receiptOnlyRoundTrip runs the old decoder/encoder so mixed-fleet tests model
// actual loss of unknown JSON fields rather than toggling Go fields manually.
func receiptOnlyRoundTrip(t *testing.T, raw string) string {
	t.Helper()
	var old receiptOnlyCrawlerTask
	require.NoError(t, json.Unmarshal([]byte(raw), &old))
	encoded, err := json.Marshal(&old)
	require.NoError(t, err)
	return string(encoded)
}

// TestCrawlerAcquisitionMixedVersionWire preserves direct successes across the
// four old/new producer-worker pairs and enables fallback only for new pairs.
func TestCrawlerAcquisitionMixedVersionWire(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		oldProducer, oldWorker bool
	}{
		{"old producer old worker", true, true},
		{"old producer new worker", true, false},
		{"new producer old worker", false, true},
		{"new producer new worker", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := CrawlerEgressPolicy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
			submitted := NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/article", "", true, &policy)
			submitted.AllowProviderFallback = !tc.oldProducer
			raw, err := submitted.ToString()
			require.NoError(t, err)
			if tc.oldProducer || tc.oldWorker {
				raw = receiptOnlyRoundTrip(t, raw)
			}
			workerTask, err := NewHTMLCrawlerTaskFromString(raw)
			require.NoError(t, err)
			workerTask.Status = TaskStatusSuccess
			workerTask.ResultHTML = []byte("<p>unchanged article</p>")
			workerTask.ResultMarkdown = []byte("unchanged article")
			if workerTask.AllowProviderFallback {
				workerTask.Acquisition = &crawleregress.Acquisition{Method: crawleregress.AcquisitionProvider, Provider: "defuddle"}
			} else {
				if !tc.oldWorker {
					workerTask.Acquisition = &crawleregress.Acquisition{Method: crawleregress.AcquisitionDirect}
				}
				workerTask.EgressReceipt = crawleregress.NewReceipt(workerTask.TaskID, workerTask.Url, *workerTask.Egress)
				workerTask.EgressReceipt.Origins = []crawleregress.Origin{{URL: workerTask.Url, Addresses: policy.Addresses, PeerAddress: "1.1.1.1", Kind: crawleregress.OriginDocument}}
				workerTask.RequestChain = []string{workerTask.Url}
			}
			raw, err = workerTask.ToString()
			require.NoError(t, err)
			if tc.oldWorker || tc.oldProducer {
				raw = receiptOnlyRoundTrip(t, raw)
			}
			result, err := NewHTMLCrawlerTaskFromString(raw)
			require.NoError(t, err)
			require.Equal(t, submitted.TaskID, result.TaskID)
			require.Equal(t, submitted.Url, result.Url)
			require.Equal(t, submitted.OutputMarkdown, result.OutputMarkdown)
			require.Equal(t, "unchanged article", string(result.ResultMarkdown))
			require.Equal(t, "<p>unchanged article</p>", string(result.ResultHTML))
			require.NoError(t, crawleregress.VerifyAcquisition(t.Context(), submitted.TaskID, submitted.Url, policy, submitted.AllowProviderFallback, result.Acquisition, result.EgressReceipt))
			if tc.oldProducer || tc.oldWorker {
				require.False(t, result.AllowProviderFallback)
				require.Nil(t, result.Acquisition)
				require.NoError(t, crawleregress.VerifyReceipt(t.Context(), submitted.TaskID, submitted.Url, policy, result.EgressReceipt))
				return
			}
			require.True(t, result.AllowProviderFallback)
			require.Equal(t, "provider", result.Acquisition.Method)
			require.Nil(t, result.EgressReceipt)
			require.Empty(t, result.RequestChain)
			// A pre-upgrade caller cannot mistake provider bytes for direct content.
			legacy, err := NewHTMLCrawlerTaskFromString(receiptOnlyRoundTrip(t, raw))
			require.NoError(t, err)
			require.ErrorIs(t, crawleregress.VerifyReceipt(t.Context(), submitted.TaskID, submitted.Url, policy, legacy.EgressReceipt), crawleregress.ErrUnverified)
		})
	}
}
