package arweave

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGatewayResponseClassification pins which gateway answers end, retry or
// complete a request, following the Arweave node and arweave-js uploader.
func TestGatewayResponseClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		classify func(int, string) gatewayOutcome
		status   int
		body     string
		want     gatewayOutcome
	}{
		{"request ok", classifyRequest, http.StatusOK, "OK", gatewaySucceeded},
		{"request rate limited", classifyRequest, http.StatusTooManyRequests, "", gatewayRetryable},
		{"request server error", classifyRequest, http.StatusServiceUnavailable, `{"error":"not_joined"}`, gatewayRetryable},
		{"request rejected", classifyRequest, http.StatusBadRequest, "", gatewayFailed},
		{"request not found", classifyRequest, http.StatusNotFound, "", gatewayFailed},
		{"tx accepted", classifyTransaction, http.StatusOK, "OK", gatewaySucceeded},
		{"tx already processed", classifyTransaction, http.StatusAlreadyReported, "Transaction already processed.", gatewaySucceeded},
		{"tx already in mempool", classifyTransaction, http.StatusBadRequest, "Transaction is already in the mempool.", gatewaySucceeded},
		{"tx already on weave", classifyTransaction, http.StatusBadRequest, "Transaction is already on the weave.\n", gatewaySucceeded},
		{"tx verification failed", classifyTransaction, http.StatusBadRequest, "Transaction verification failed.", gatewayFailed},
		{"tx overspend", classifyTransaction, http.StatusPaymentRequired, "Overspend", gatewayFailed},
		{"tx gateway error", classifyTransaction, http.StatusBadGateway, "", gatewayRetryable},
		{"chunk stored", classifyChunk, http.StatusOK, "", gatewaySucceeded},
		{"chunk stored temporarily", classifyChunk, http.StatusSeeOther, "", gatewaySucceeded},
		{"chunk invalid proof", classifyChunk, http.StatusBadRequest, `{"error":"invalid_proof"}`, gatewayFailed},
		{"chunk too big", classifyChunk, http.StatusBadRequest, `{"error":"chunk_too_big"}`, gatewayFailed},
		{"chunk disk full", classifyChunk, http.StatusBadRequest, `{"error":"disk_full"}`, gatewayFailed},
		{"chunk data root not yet known", classifyChunk, http.StatusBadRequest, `{"error":"data_root_not_found"}`, gatewayRetryable},
		{"chunk disk pool full", classifyChunk, http.StatusBadRequest, `{"error":"exceeds_disk_pool_size_limit"}`, gatewayRetryable},
		{"chunk node busy", classifyChunk, http.StatusServiceUnavailable, `{"error":"timeout"}`, gatewayRetryable},
		{"chunk body too large", classifyChunk, http.StatusRequestEntityTooLarge, "Payload too large", gatewayFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.classify(tc.status, tc.body))
		})
	}
}

// TestGatewayRetryDelay checks capped exponential backoff with equal jitter,
// and Retry-After.
func TestGatewayRetryDelay(t *testing.T) {
	g := newGatewayClient("https://gateway.example")
	inHalf := func(t *testing.T, backoff time.Duration, attempt int, retryAfter time.Duration, msg string) {
		t.Helper()
		seen := map[time.Duration]bool{}
		for range 200 {
			d := g.retryDelay(attempt, retryAfter)
			require.GreaterOrEqual(t, d, backoff/2, msg)
			require.LessOrEqual(t, d, backoff, msg)
			seen[d] = true
		}
		require.Greater(t, len(seen), 1, "%s: the delay must be jittered", msg)
	}
	inHalf(t, time.Second, 1, 0, "first retry")
	inHalf(t, 2*time.Second, 2, 0, "second retry")
	inHalf(t, 16*time.Second, 5, 0, "fifth retry")
	inHalf(t, gatewayRetryMaxDelay, 6, 0, "capped")
	inHalf(t, gatewayRetryMaxDelay, 200, 0, "large attempts must not overflow")
	inHalf(t, 2*time.Second, 2, 500*time.Millisecond, "a shorter Retry-After does not cut the backoff")

	require.Equal(t, 10*time.Second, g.retryDelay(1, 10*time.Second), "a longer Retry-After is honored exactly")
	require.Equal(t, gatewayRetryMaxDelay, g.retryDelay(1, time.Hour), "Retry-After is capped")

	require.Equal(t, 5*time.Second, parseRetryAfter(" 5 "))
	for _, v := range []string{"", "0", "-1", "soon", "Wed, 21 Oct 2026 07:28:00 GMT"} {
		require.Zero(t, parseRetryAfter(v), v)
	}
}
