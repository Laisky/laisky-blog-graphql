package search

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
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

// TestProviderFallbackRejectsForgedResults keeps the submitted identity and
// content contract intact when an acquisition came from a provider.
func TestProviderFallbackRejectsForgedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*rlibs.HTMLCrawlerTask)
		code   string
	}{
		{"wrong task", func(task *rlibs.HTMLCrawlerTask) { task.TaskID = "other-task" }, "crawler_egress_rejected"},
		{"wrong target", func(task *rlibs.HTMLCrawlerTask) { task.Url += "/other" }, "crawler_egress_rejected"},
		{"wrong output", func(task *rlibs.HTMLCrawlerTask) { task.OutputMarkdown = false }, "crawler_egress_rejected"},
		{"missing policy", func(task *rlibs.HTMLCrawlerTask) { task.Egress = nil }, "crawler_egress_rejected"},
		{"changed policy", func(task *rlibs.HTMLCrawlerTask) { task.Egress.MaxRedirects++ }, "crawler_egress_rejected"},
		{"missing echoed capability", func(task *rlibs.HTMLCrawlerTask) { task.AllowProviderFallback = false }, "crawler_egress_rejected"},
		{"missing provenance", func(task *rlibs.HTMLCrawlerTask) { task.Acquisition = nil }, "crawler_egress_unverified"},
		{"provider masquerades as direct", func(task *rlibs.HTMLCrawlerTask) {
			task.Acquisition = &crawleregress.Acquisition{Method: crawleregress.AcquisitionDirect}
		}, "crawler_egress_unverified"},
		{"fabricated direct chain", func(task *rlibs.HTMLCrawlerTask) { task.RequestChain = []string{task.Url} }, "crawler_egress_rejected"},
		{"fabricated direct receipt", func(task *rlibs.HTMLCrawlerTask) {
			task.EgressReceipt = crawleregress.NewReceipt(task.TaskID, task.Url, *task.Egress)
		}, "crawler_egress_rejected"},
		{"invalid provider", func(task *rlibs.HTMLCrawlerTask) { task.Acquisition.Provider = "https://private.invalid/canary" }, "crawler_egress_rejected"},
		{"unknown method", func(task *rlibs.HTMLCrawlerTask) { task.Acquisition.Method = "unknown" }, "crawler_egress_rejected"},
		{"empty content", func(task *rlibs.HTMLCrawlerTask) { task.ResultMarkdown = nil }, "crawler_result_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) {
				providerWireResult(t, task)
				tc.mutate(task)
			}}
			body, err := fetchDynamicURLContent(t.Context(), store, "https://1.1.1.1/article", "synthetic-caller-key", true)
			require.Error(t, err)
			require.Empty(t, body)
			require.Equal(t, tc.code, FetchDiagnostic(err).ErrorCode)
			require.NotContains(t, err.Error(), "canary")
		})
	}
}

// TestProviderFallbackCannotEscalateCapability verifies that returned metadata
// cannot opt a direct-only caller into accepting a provider body.
func TestProviderFallbackCannotEscalateCapability(t *testing.T) {
	policy := crawleregress.Policy{Host: "1.1.1.1", Addresses: []string{"1.1.1.1"}, MaxRedirects: 1}
	task := rlibs.NewHTMLCrawlerTaskWithEgress("https://1.1.1.1/article", "", true, &policy)
	providerWireResult(t, task)
	require.ErrorIs(t, verifyAcquiredTask(t.Context(), task.TaskID, task.Url, policy, false, task), crawleregress.ErrRejected)
	require.NoError(t, verifyAcquiredTask(t.Context(), task.TaskID, task.Url, policy, true, task))
}

// TestProviderFallbackLifecycle reports provider acquisition without asserting
// that the application verified a direct connection to the original target.
func TestProviderFallbackLifecycle(t *testing.T) {
	var output bytes.Buffer
	logger, err := logSDK.NewWithName("reviewed", logSDK.LevelInfo, zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zapcore.InfoLevel)
	}))
	require.NoError(t, err)
	store := &syntheticCrawlerStore{mutate: func(task *rlibs.HTMLCrawlerTask) { providerWireResult(t, task) }}
	body, err := fetchDynamicURLContent(WithFetchLogger(t.Context(), logger), store, "https://1.1.1.1/article", "synthetic-caller-key", true)
	require.NoError(t, err)
	require.Equal(t, "# Provider article", string(body))
	accepted := 0
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		require.NotEqual(t, "result_verified", event["event"])
		if event["event"] == "result_accepted" {
			accepted++
			require.Equal(t, "provider", event["acquisition_method"])
			require.Equal(t, "jina", event["provider"])
		}
	}
	require.Equal(t, 1, accepted)
	require.NotContains(t, output.String(), "Provider article")
	require.NotContains(t, output.String(), "synthetic-caller-key")
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
