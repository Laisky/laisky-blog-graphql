package search

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// providerWireResult injects the additive worker acquisition fields through the
// real task decoder so the regression also runs against the old task schema.
func providerWireResult(t *testing.T, task *rlibs.HTMLCrawlerTask) {
	t.Helper()
	task.EgressReceipt = nil
	task.RequestChain = nil
	task.ResultHTML = []byte("<h1>Provider article</h1>\n")
	task.ResultMarkdown = []byte("# Provider article")
	raw, err := task.ToString()
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &envelope))
	envelope["allow_provider_fallback"] = json.RawMessage(`true`)
	envelope["acquisition"] = json.RawMessage(`{"method":"provider","provider":"jina"}`)
	rawBytes, err := json.Marshal(envelope)
	require.NoError(t, err)
	decoded, err := rlibs.NewHTMLCrawlerTaskFromString(string(rawBytes))
	require.NoError(t, err)
	*task = *decoded
}

// TestProviderFallbackResultReachesCaller reproduces the direct-receipt-only
// consumer rejecting valid provider content in both supported output formats.
func TestProviderFallbackResultReachesCaller(t *testing.T) {
	for _, markdown := range []bool{true, false} {
		t.Run(map[bool]string{true: "markdown", false: "html"}[markdown], func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
				providerWireResult(t, task)
			}}
			body, err := fetchDynamicURLContent(t.Context(), store, "https://1.1.1.1/article", "synthetic-caller-key", markdown)
			require.NoError(t, err)
			if markdown {
				require.Equal(t, "# Provider article", string(body))
			} else {
				require.Equal(t, "<h1>Provider article</h1>\n", string(body))
			}
			require.Empty(t, store.task.APIKey)
			require.Nil(t, store.task.EgressReceipt, "provider content must not claim direct target evidence")
		})
	}
}
