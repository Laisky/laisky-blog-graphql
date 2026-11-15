package arweave

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// uploadWatchdog bounds how long a test waits for Upload, so an upload that
// ignores cancellation or hangs fails the test instead of the whole run.
const uploadWatchdog = 10 * time.Second

type uploadResult struct {
	id  string
	err error
}

func startUpload(ctx context.Context, a *Ardrive, payload []byte) <-chan uploadResult {
	done := make(chan uploadResult, 1)
	go func() {
		id, err := a.Upload(ctx, payload)
		done <- uploadResult{id: id, err: err}
	}()
	return done
}

func awaitUpload(t *testing.T, done <-chan uploadResult) uploadResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(uploadWatchdog):
		require.FailNow(t, "Upload did not return", "still running after %s", uploadWatchdog)
		return uploadResult{}
	}
}

func randomPayload(t *testing.T, size int) []byte {
	t.Helper()
	payload := make([]byte, size)
	_, err := rand.Read(payload)
	require.NoError(t, err)
	return payload
}

// TestArdriveUploadCanceledContextSendsNothing: a caller that has already
// given up must not spend gateway requests, let alone a reward.
func TestArdriveUploadCanceledContextSendsNothing(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := awaitUpload(t, startUpload(ctx, newTestArdrive(t, walletPath, gw.url), []byte("never sent")))
	require.ErrorIs(t, r.err, context.Canceled)
	require.Empty(t, r.id)
	require.Zero(t, gw.totalHits())
}

// TestArdriveUploadCancellationAbortsInFlightRequest: before the signed
// transaction is sent, cancellation must reach the request on the wire, not
// only the gaps between requests.
func TestArdriveUploadCancellationAbortsInFlightRequest(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failEvery(routePrice, gatewayFault{stall: true})
	arrived := make(chan struct{}, 1)
	gw.onRequest(routePrice, func(int) { arrived <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := startUpload(ctx, newTestArdrive(t, walletPath, gw.url), []byte("abandoned"))
	select {
	case <-arrived:
	case <-time.After(uploadWatchdog):
		require.FailNow(t, "price request never reached the gateway")
	}
	cancel()

	r := awaitUpload(t, done)
	require.ErrorIs(t, r.err, context.Canceled)
	require.Empty(t, r.id)
	require.Eventually(t, func() bool { return gw.abandonedCount(routePrice) == 1 }, uploadWatchdog, 10*time.Millisecond,
		"the stalled request itself must be canceled")
	require.Equal(t, 1, gw.hitCount(routePrice), "a canceled caller is not retried")
	require.Zero(t, gw.hitCount(routeTx), "nothing is paid for after cancellation")
}

// TestArdriveUploadFinishesCommittedUploadAfterCancellation: once the signed
// transaction is sent, the reward is spent when it is mined. Abandoning the
// chunks then would pay for data that never arrives, so the upload finishes
// and returns its ID, as it always did before cancellation was supported.
func TestArdriveUploadFinishesCommittedUploadAfterCancellation(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	for _, tc := range []struct {
		name  string
		route string
		at    int
	}{
		{name: "while the transaction is sent", route: routeTx, at: 1},
		{name: "between chunks", route: routeChunk, at: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakeGateway(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gw.onRequest(tc.route, func(n int) {
				if n == tc.at {
					cancel()
				}
			})
			payload := randomPayload(t, 600<<10)

			r := awaitUpload(t, startUpload(ctx, newTestArdrive(t, walletPath, gw.url), payload))
			require.NoError(t, r.err)
			require.Error(t, ctx.Err(), "the caller was canceled mid-upload")
			tx := gw.onlyTx(t)
			require.Equal(t, tx.ID, r.id)
			require.Equal(t, payload, gw.deliveredData(t, tx))
			require.NoError(t, verifyArweaveSignature(t, tx, pub))
		})
	}
}

// TestArdriveUploadBoundsStalledRequests: a gateway that never answers must
// not hold an upload forever, even when the caller sets no deadline.
func TestArdriveUploadBoundsStalledRequests(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failEvery(routeAnchor, gatewayFault{stall: true})
	a := newTestArdrive(t, walletPath, gw.url)
	setRequestBounds(a, 100*time.Millisecond, 3)

	r := awaitUpload(t, startUpload(context.Background(), a, []byte("stalled")))
	require.ErrorIs(t, r.err, context.DeadlineExceeded)
	require.Empty(t, r.id)
	require.Equal(t, 3, gw.hitCount(routeAnchor), "each stalled attempt is retried up to the attempt limit")
	require.Eventually(t, func() bool { return gw.abandonedCount(routeAnchor) == 3 }, uploadWatchdog, 10*time.Millisecond)
	require.Zero(t, gw.hitCount(routeTx))
}

// TestArdriveUploadBoundsCommittedPhase: finishing a committed upload ignores
// the caller, so it needs its own bound, and its failure must name the
// transaction whose data is incomplete.
func TestArdriveUploadBoundsCommittedPhase(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failEvery(routeChunk, gatewayFault{stall: true})
	a := newTestArdrive(t, walletPath, gw.url)
	setCommitTimeout(a, 300*time.Millisecond)

	r := awaitUpload(t, startUpload(context.Background(), a, []byte("never stored")))
	require.ErrorIs(t, r.err, context.DeadlineExceeded)
	require.ErrorContains(t, r.err, gw.onlyTx(t).ID)
	require.Empty(t, r.id)
}

// TestArdriveUploadRetriesTransientFailures: rate limits, gateway errors and
// the node's retryable chunk errors are retried, so a brief outage does not
// strand a paid transaction without its data.
func TestArdriveUploadRetriesTransientFailures(t *testing.T) {
	walletPath, pub := writeTestWallet(t)
	gw := newFakeGateway(t)
	gw.failRequest(routePrice, 1, gatewayFault{status: http.StatusServiceUnavailable, body: `{"error":"timeout"}`})
	gw.failRequest(routeAnchor, 1, gatewayFault{status: http.StatusTooManyRequests, retryAfter: "0"})
	gw.failRequest(routeTx, 1, gatewayFault{status: http.StatusBadGateway})
	gw.failRequest(routeChunk, 2, gatewayFault{status: http.StatusBadRequest, body: `{"error":"data_root_not_found"}`})
	gw.failRequest(routeChunk, 3, gatewayFault{status: http.StatusServiceUnavailable, body: `{"error":"not_joined"}`})
	payload := randomPayload(t, 600<<10)

	r := awaitUpload(t, startUpload(context.Background(), newTestArdrive(t, walletPath, gw.url), payload))
	require.NoError(t, r.err)
	tx := gw.onlyTx(t)
	require.Equal(t, tx.ID, r.id)
	require.Equal(t, payload, gw.deliveredData(t, tx))
	require.Equal(t, canonicalChunkRanges(len(payload)), gw.chunkRanges(tx))
	require.Equal(t, 2, gw.hitCount(routePrice))
	require.Equal(t, 2, gw.hitCount(routeAnchor))
	require.Equal(t, 2, gw.hitCount(routeTx))
	require.Equal(t, 5, gw.hitCount(routeChunk))
	require.NoError(t, verifyArweaveSignature(t, tx, pub))
}

// TestArdriveUploadTreatsResentTransactionAsAccepted: if the reply to POST /tx
// is lost after the gateway accepted it, the retry finds the transaction
// already known. Its ID is fixed by the signature, so that is a success.
func TestArdriveUploadTreatsResentTransactionAsAccepted(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	for _, tc := range []struct {
		name  string
		retry gatewayFault
	}{
		{name: "already processed (208)"},
		{name: "already in the mempool", retry: gatewayFault{status: http.StatusBadRequest, body: "Transaction is already in the mempool."}},
		{name: "already on the weave", retry: gatewayFault{status: http.StatusBadRequest, body: "Transaction is already on the weave."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakeGateway(t)
			gw.failRequest(routeTx, 1, gatewayFault{status: http.StatusBadGateway, serveFirst: true})
			if tc.retry.status != 0 {
				gw.failRequest(routeTx, 2, tc.retry)
			}
			payload := randomPayload(t, 300<<10)

			r := awaitUpload(t, startUpload(context.Background(), newTestArdrive(t, walletPath, gw.url), payload))
			require.NoError(t, r.err)
			tx := gw.onlyTx(t)
			require.Equal(t, tx.ID, r.id)
			require.Equal(t, 2, gw.hitCount(routeTx))
			require.Equal(t, payload, gw.deliveredData(t, tx))
		})
	}
}

// TestArdriveUploadRejectsInvalidGatewayQuotes: a reward or anchor that cannot
// form a valid transaction must stop the upload before anything is signed and
// sent, instead of posting a transaction the network will reject.
func TestArdriveUploadRejectsInvalidGatewayQuotes(t *testing.T) {
	walletPath, _ := writeTestWallet(t)
	for _, tc := range []struct {
		name           string
		reward, anchor string
	}{
		{name: "zero reward", reward: "0"},
		{name: "negative reward", reward: "-5"},
		{name: "non-numeric reward", reward: "free"},
		{name: "empty anchor", anchor: " "},
		{name: "anchor is not base64url", anchor: "not+base64/url=="},
		{name: "anchor has the wrong length", anchor: base64.RawURLEncoding.EncodeToString([]byte("short"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakeGateway(t)
			if tc.reward != "" {
				gw.setReward(tc.reward)
			}
			if tc.anchor != "" {
				gw.setAnchor(tc.anchor)
			}

			r := awaitUpload(t, startUpload(context.Background(), newTestArdrive(t, walletPath, gw.url), []byte("quote")))
			require.Error(t, r.err)
			require.Empty(t, r.id)
			require.Zero(t, gw.hitCount(routeTx))
			require.Zero(t, gw.txCount())
		})
	}
}
