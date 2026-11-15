package pageindex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// flakyResponsesServer fails the first failures requests with 503 and answers the
// rest with a complete summary billed at total tokens.
func flakyResponsesServer(t *testing.T, failures int32, total int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) <= failures {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"overloaded","type":"server_error"}}`)
			return
		}
		_, _ = io.WriteString(w, responsesBody("completed", "null", "A generated summary.", usageJSON(total-10, 10, total), 0))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestIndexStatsCountEveryUpstreamRequest reproduces the P3 finding of the third
// review of #96: Stats counted only calls that returned a response, so a call
// that was retried, or never answered, spent budget that its run's statistics did
// not show. Stats now count every upstream request, its retries, and the tokens
// charged for them, split into provider-reported and estimated charges, and the
// charges add up to what the budgets were charged.
func TestIndexStatsCountEveryUpstreamRequest(t *testing.T) {
	tok, err := NewTokenizer("gpt-5.4-mini")
	require.NoError(t, err)
	doc := []byte("# Only\n\nOne section body.\n")
	index := func(t *testing.T, llm LLM) (*Stats, int64) {
		t.Helper()
		cfg := summarizingSettings()
		spend := frozenSpend(cfg.Limits)
		idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: spend})
		require.NoError(t, err)
		_, stats, err := idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "doc", Tenant: "t"}, nil)
		require.NoError(t, err)
		spend.mu.Lock()
		defer spend.mu.Unlock()
		return stats, spend.global.tokens
	}

	t.Run("retried and answered", func(t *testing.T) {
		srv, hits := flakyResponsesServer(t, 2, 500)
		llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: srv.URL, Retry: fastRetry(3), Tokenizer: tok})
		require.NoError(t, err)
		stats, charged := index(t, llm)
		require.Equal(t, int32(3), hits.Load())
		require.Equal(t, 1, stats.LLMCalls, "one call returned a response")
		require.Equal(t, 3, stats.UpstreamRequests, "three requests were sent")
		require.Equal(t, 2, stats.Retries)
		require.Equal(t, int64(500), stats.ReportedTokens, "the answer was billed as reported")
		require.Positive(t, stats.EstimatedTokens, "the two error responses were charged their input estimate")
		require.Equal(t, charged, stats.ReportedTokens+stats.EstimatedTokens, "statistics match what the budgets were charged")
	})

	t.Run("never answered", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		closed.Close()
		llm, err := NewOpenAILLM(LLMConfig{APIKey: "sk-test", BaseURL: closed.URL, Retry: fastRetry(2), Tokenizer: tok})
		require.NoError(t, err)
		cfg := summarizingSettings()
		spend := frozenSpend(cfg.Limits)
		idx, err := NewIndexer(Deps{LLM: llm, Tokenizer: tok, Settings: cfg, Spend: spend})
		require.NoError(t, err)
		_, stats, err := idx.Index(context.Background(), KindMarkdown, doc, IndexOptions{DocID: "doc", Tenant: "t"}, nil)
		if err == nil {
			t.Log("the run degraded instead of failing")
		}
		require.NotNil(t, stats)
		require.Zero(t, stats.LLMCalls, "no response was received")
		require.Equal(t, 2, stats.UpstreamRequests, "both attempts were sent")
		require.Equal(t, 1, stats.Retries)
		require.Zero(t, stats.ReportedTokens)
		spend.mu.Lock()
		charged := spend.global.tokens
		spend.mu.Unlock()
		require.Equal(t, charged, stats.EstimatedTokens, "each unanswered request is charged its whole reservation")
	})
}
