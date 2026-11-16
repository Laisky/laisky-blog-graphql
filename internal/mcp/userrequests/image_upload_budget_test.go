package userrequests

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin the end-to-end time budget of an image create
// (maintainer review of PR #94, P2): up to MaxPerRequest objects are uploaded
// inside the same request deadline that also covers reading the body,
// normalizing and the finalize transaction.

// scaleUploadBudget shrinks the create deadline and its upload phases so a
// slow store can be simulated in milliseconds. The previous values are
// restored when the test ends.
func scaleUploadBudget(t *testing.T, deadline, reserve, window time.Duration) {
	t.Helper()
	prevDeadline, prevReserve, prevWindow := createRequestTimeout, imageFinalizeReserve, imageMinUploadWindow
	createRequestTimeout, imageFinalizeReserve, imageMinUploadWindow = deadline, reserve, window
	t.Cleanup(func() {
		createRequestTimeout, imageFinalizeReserve, imageMinUploadWindow = prevDeadline, prevReserve, prevWindow
	})
}

// slowPuts makes every Put of h's store take latency (or end with its
// context) and counts the Puts in flight at once.
func slowPuts(h *orphanHarness, latency time.Duration) (maxInFlight *atomic.Int64) {
	var inFlight atomic.Int64
	maxInFlight = &atomic.Int64{}
	h.store.BeforePut = func(ctx context.Context, _ string) error {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := maxInFlight.Load()
			if n <= seen || maxInFlight.CompareAndSwap(seen, n) {
				break
			}
		}
		select {
		case <-time.After(latency):
			return nil
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), "slow put canceled")
		}
	}
	return maxInFlight
}

// TestCreateWithImagesUploadsFitTheRequestDeadline reproduces the review's
// finding: five images uploaded one after another, each allowed up to the
// per-object timeout, outlast a request deadline that one parallel round
// would meet. Against a store that takes 300 ms per PUT and a 1.2 s
// deadline, sequential uploads need 1.5 s and fail with storage_unavailable;
// bounded parallel uploads finish in two rounds.
func TestCreateWithImagesUploadsFitTheRequestDeadline(t *testing.T) {
	scaleUploadBudget(t, 1200*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond)
	h := newOrphanHarness(t, defaultImageSettings(), "")
	maxInFlight := slowPuts(h, 300*time.Millisecond)

	files := [][]byte{testPNG(t, 11, 7), testPNG(t, 12, 7), testPNG(t, 13, 7), testPNG(t, 14, 7), testPNG(t, 15, 7)}
	start := time.Now()
	rec := h.post(t, context.Background(), "sk-upload-deadline-1234", files...)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.PutKeys(), len(files), "every distinct image is uploaded once")
	require.Greater(t, maxInFlight.Load(), int64(1), "uploads must overlap")
	require.LessOrEqual(t, maxInFlight.Load(), int64(imageUploadConcurrency), "uploads stay bounded")
	require.Less(t, elapsed, createRequestTimeout)
	h.requireNoUnreferencedObjects(t)
}

// TestCreateWithImagesRefusesWithoutTimeToUpload: a create whose deadline
// leaves less than the minimum upload window after preparing is refused with
// image_busy before anything is reserved or uploaded, instead of reserving
// quota, starting PUTs that cannot finish and compensating them.
func TestCreateWithImagesRefusesWithoutTimeToUpload(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	auth := mustAuth(t, "sk-upload-late-12345678")
	prepared, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{testPNG(t, 9, 9)}))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), imageFinalizeReserve+imageMinUploadWindow/2)
	defer cancel()
	_, err = h.svc.CreateRequestWithImages(ctx, auth, "late", "", prepared, h.images)
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrImageBusy), "got %v", err)
	require.Empty(t, h.store.PutKeys(), "no PUT may start without time to finish")
	require.Zero(t, h.claimCount(t, ""), "nothing reserved")
}

// TestCreateWithImagesUploadsEndBeforeFinalizeReserve: uploads must end
// imageFinalizeReserve before the request deadline, so a PUT that would run
// into the time the finalize transaction needs is canceled instead, and the
// failure is compensated while the request context is still alive.
func TestCreateWithImagesUploadsEndBeforeFinalizeReserve(t *testing.T) {
	scaleUploadBudget(t, time.Second, 400*time.Millisecond, 100*time.Millisecond)
	h := newOrphanHarness(t, defaultImageSettings(), "")
	var putDeadline atomic.Int64
	h.store.BeforePut = func(ctx context.Context, _ string) error {
		if d, ok := ctx.Deadline(); ok {
			putDeadline.Store(d.UnixNano())
		}
		<-ctx.Done()
		return errs.Wrap(ctx.Err(), "put outlasted its window")
	}

	start := time.Now()
	rec := h.post(t, context.Background(), "sk-upload-reserve-12345", testPNG(t, 10, 10))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, "storage_unavailable", errorCode(t, rec))
	require.NotZero(t, putDeadline.Load())
	window := time.Unix(0, putDeadline.Load()).Sub(start)
	require.Less(t, window, createRequestTimeout-imageFinalizeReserve+50*time.Millisecond,
		"the PUT deadline must leave the finalize reserve")
	h.requireEveryObjectDiscoverable(t)
}

// TestCreateWithImagesFailedUploadCancelsTheOthers: when one parallel PUT
// fails, the PUTs still in flight are canceled at once rather than left to
// run to their own timeout, and every started PUT keeps its late-write
// re-check.
func TestCreateWithImagesFailedUploadCancelsTheOthers(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	var calls atomic.Int64
	allStarted := make(chan struct{})
	h.store.BeforePut = func(ctx context.Context, _ string) error {
		n := calls.Add(1)
		if n == 3 {
			close(allStarted)
		}
		if n == 1 {
			// Fail only once every sibling is in flight.
			select {
			case <-allStarted:
			case <-ctx.Done():
			}
			return errs.New("injected store failure")
		}
		select {
		case <-time.After(imageObjectPutTimeout):
			return nil
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), "canceled after a sibling failed")
		}
	}

	start := time.Now()
	rec := h.post(t, context.Background(), "sk-upload-cancel-123456", testPNG(t, 21, 9), testPNG(t, 22, 9), testPNG(t, 23, 9))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.Less(t, time.Since(start), 5*time.Second, "siblings of a failed PUT must not run to their own timeout")
	require.Equal(t, int64(3), calls.Load(), "every upload had started")
	require.Equal(t, 3, h.claimCount(t, claimOrphan), "each started PUT keeps its late-write re-check")
	h.requireEveryObjectDiscoverable(t)
}
