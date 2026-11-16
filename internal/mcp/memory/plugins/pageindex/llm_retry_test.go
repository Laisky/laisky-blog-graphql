package pageindex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// statusServer answers every Responses-API request with status and counts them.
func statusServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"refused","type":"test"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// fastRetry keeps retry tests quick.
func fastRetry(attempts int) RetrySettings {
	return RetrySettings{MaxAttempts: attempts, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

// TestOpenAILLMDoesNotRetryClientErrors covers review item 6 of #96: every error
// was retried, so a request the provider rejects (400, 401, 404) was sent up to
// ten times. Only timeouts, 408, 409, 429 and 5xx are retried.
func TestOpenAILLMDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		srv, hits := statusServer(t, status)
		llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(5)})
		require.NoError(t, err)
		_, err = llm.Respond(context.Background(), Request{Input: userInput("hi")})
		require.Error(t, err)
		require.Equal(t, int32(1), hits.Load(), "status %d is not retried", status)
	}
	srv, hits := statusServer(t, http.StatusServiceUnavailable)
	llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(5)})
	require.NoError(t, err)
	_, err = llm.Respond(context.Background(), Request{Input: userInput("hi")})
	require.Error(t, err)
	require.Equal(t, int32(5), hits.Load(), "a 503 is retried up to max_attempts")
}

// TestOpenAILLMFromSettingsUsesRetryPolicy checks that the server's client takes
// the documented indexer.retry.* settings, whose default is a small budget.
func TestOpenAILLMFromSettingsUsesRetryPolicy(t *testing.T) {
	srv, hits := statusServer(t, http.StatusInternalServerError)
	settings := Settings{LLM: LLMSettings{APIKey: "sk-test", BaseURL: srv.URL, IndexingModel: "gpt-5.4-mini"}, Indexer: IndexerSettings{Retry: fastRetry(2)}}
	llm, err := NewOpenAILLMFromSettings(settings, nil, nil)
	require.NoError(t, err)
	_, err = llm.Respond(context.Background(), Request{Input: userInput("hi")})
	require.Error(t, err)
	require.Equal(t, int32(2), hits.Load())

	srv, hits = statusServer(t, http.StatusInternalServerError)
	llm, err = NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: RetrySettings{InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}})
	require.NoError(t, err)
	_, err = llm.Respond(context.Background(), Request{Input: userInput("hi")})
	require.Error(t, err)
	require.Equal(t, int32(DefaultLLMRetryAttempts), hits.Load(), "an unset policy uses the small default")
}

// TestEveryUpstreamAttemptIsAdmitted checks that retries draw from the same
// budgets as calls: with a per-document ceiling of two upstream requests, a
// failing upstream is sent exactly two requests, both counted by the rolling
// budget, and the leaf degrades to a local summary instead of failing the write.
func TestEveryUpstreamAttemptIsAdmitted(t *testing.T) {
	srv, hits := statusServer(t, http.StatusInternalServerError)
	llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(5)})
	require.NoError(t, err)
	cfg := summarizingSettings()
	cfg.Limits = LimitsSettings{MaxLLMCallsPerDocument: 2}
	spend := frozenSpend(cfg.Limits)
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: spend})
	require.NoError(t, err)

	tree, stats, err := idx.Index(context.Background(), KindMarkdown, []byte("# Only\n\nOne section body.\n"), IndexOptions{DocID: "retry", Tenant: "t"}, nil)
	require.NoError(t, err)
	require.Equal(t, int32(2), hits.Load(), "the third attempt is refused before it is sent")
	require.Equal(t, uint64(2), spend.Stats().Admitted, "every upstream request is admitted")
	require.Equal(t, "Only One section body.", tree.Structure[0].Summary)
	require.Equal(t, 1, stats.LocalSummaries)
}
