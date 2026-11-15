package userrequests

import (
	"context"
	"fmt"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/Laisky/zap/zapcore"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// The tests in this file reproduce what an independent review of the PR #94
// third-round fixes found; each was red on 2be5333.

// TestReconcileTickKeepsDrainingPastAFailingKey: one key whose delete keeps
// failing ended every tick after its first pass, so the backlog behind it
// drained at one batch per interval again. The failed key holds a live
// lease, so later passes of the same tick work on other claims.
func TestReconcileTickKeepsDrainingPastAFailingKey(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	h.setClock(reconcileNow.Add(-time.Minute)) // the failing key's claim is the oldest
	stuck := crashAfterUpload(t, h, "sk-stuck-key-1234567890", testPNG(t, 61, 9))[0].StorageKey
	h.setClock(reconcileNow)
	for i := range 6 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-behind-%d-1234567890", i), testPNG(t, 62+i, 9))
	}
	h.store.BeforeDelete = func(_ context.Context, key string) error {
		if key == stuck {
			return errs.New("injected permanent delete failure")
		}
		return nil
	}
	h.setClock(reconcileNow.Add(imageUploadClaimTTL + time.Hour))

	result, backlog, err := h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.Error(t, err, "the failing key is still reported")
	require.Equal(t, 6, result.Deleted, "every other object is deleted in the same tick")
	require.Equal(t, []string{stuck}, h.store.Keys())
	require.Zero(t, backlog.Due, "only the failing key's live lease is left")
}

// TestReconcileTickStopsWhenNothingSettles: when no key of a pass can be
// settled (the store is down), the tick stops instead of running every pass
// against it.
func TestReconcileTickStopsWhenNothingSettles(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	for i := range 6 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-down-%d-1234567890", i), testPNG(t, 70+i, 9))
	}
	h.store.DeleteErr = errs.New("store down")
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	result, _, err := h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.Error(t, err)
	require.Equal(t, 1, result.Passes)
}

// TestReconcileTickStopsAtShutdown: the tick checks ctx between passes.
func TestReconcileTickStopsAtShutdown(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 1
	h := newOrphanHarness(t, settings, "")
	for i := range 3 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-shutdown-%d-12345678", i), testPNG(t, 80+i, 9))
	}
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	ctx, cancel := context.WithCancel(context.Background())
	h.store.BeforeDelete = func(context.Context, string) error {
		cancel() // shutdown begins during the first pass
		return nil
	}
	result, _, err := h.svc.ReconcileImageObjectsTick(ctx, h.manager())
	require.NoError(t, err, "the started pass finishes")
	require.Equal(t, 1, result.Passes)
	require.Equal(t, 1, result.Deleted)
}

// TestLifecycleControllerReportsPeriodicErasure: a rule that another writer
// erases between checks used to be reinstalled silently (the check returned
// success, the gauge stayed 1). Each reinstall is now counted and a WARN, and
// an ERROR once it happened at three checks in a row.
func TestLifecycleControllerReportsPeriodicErasure(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepInterval = time.Hour
	fastLifecycleChecks(t, &settings)
	settings.LifecycleCheckInterval = 20 * time.Millisecond
	store := storage.NewFakeStore()
	reinstallsBefore := testutil.ToFloat64(imageMetrics.lifecycleReinstalls)
	observed := startController(t, store, settings)
	prefix := settings.Prefix + "/"

	for writes := 1; writes <= imageLifecycleMissesBeforeError; writes++ {
		require.Eventually(t, func() bool { return store.LifecycleWrites() == writes }, 2*time.Second, time.Millisecond)
		store.RemoveLifecycle(prefix)
	}
	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.ErrorLevel, "keeps being erased") == 1
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, imageLifecycleMissesBeforeError-1, countMessages(observed, zapcore.WarnLevel, "erased by another writer"))
	require.InDelta(t, imageLifecycleMissesBeforeError, testutil.ToFloat64(imageMetrics.lifecycleReinstalls)-reinstallsBefore, 0)
}

// TestLifecycleControllerReportsEachDistinctProblem: a failed check and a
// missing rule are different problems. After a transient read failure, a
// rule found really missing used to be logged at DEBUG only.
func TestLifecycleControllerReportsEachDistinctProblem(t *testing.T) {
	settings := defaultImageSettings()
	settings.LifecycleMode = LifecycleModeVerify
	settings.OrphanSweepInterval = time.Hour
	fastLifecycleChecks(t, &settings)
	store := storage.NewFakeStore()
	store.LifecycleCheckErr = errs.New("transient read failure")
	observed := startController(t, store, settings)
	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.ErrorLevel, "check image bucket lifecycle rule failed") == 1
	}, 2*time.Second, time.Millisecond)

	// readable now, and the rule is missing; the controller is running, so
	// the change goes through Inject
	store.Inject(func(s *storage.FakeStore) { s.LifecycleCheckErr = nil })
	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.ErrorLevel, "lifecycle_mode is verify") == 1
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, 1, countMessages(observed, zapcore.ErrorLevel, "check image bucket lifecycle rule failed"))
}

// TestLifecycleControllerWithoutPeriodicChecks: lifecycle_check_seconds 0
// keeps the start check and the first re-check (which catches a concurrent
// installation that erased the rule) and nothing after.
func TestLifecycleControllerWithoutPeriodicChecks(t *testing.T) {
	settings := defaultImageSettings()
	fastLifecycleChecks(t, &settings)
	settings.LifecycleCheckInterval = 0
	store := storage.NewFakeStore()
	startController(t, store, settings)
	require.Eventually(t, func() bool { return store.LifecycleCalls() == 2 }, 2*time.Second, time.Millisecond)
	time.Sleep(30 * time.Millisecond) //nolint:forbidigo // no further check may run
	require.Equal(t, 2, store.LifecycleCalls())
}

// TestCreateWithImagesUploadPanicIsCompensated: uploads run in their own
// goroutines, where a panic is outside the HTTP server's recovery and used to
// end the process. It now fails the create like an upload error, and the
// reservation is compensated.
func TestCreateWithImagesUploadPanicIsCompensated(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	auth := mustAuth(t, "sk-upload-panic-1234567")
	images, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{testPNG(t, 33, 9), testPNG(t, 34, 9)}))
	require.NoError(t, err)
	writer := scriptedWriter{h: h, put: func(ctx context.Context, auth *askuser.AuthorizationContext, img UploadedImage) error {
		if img.StorageKey == images[1].StorageKey {
			panic("injected upload panic")
		}
		return h.manager().PutImage(ctx, auth, img)
	}}
	require.NotPanics(t, func() {
		_, err = h.svc.CreateRequestWithImages(context.Background(), auth, "panic", "", images, writer)
	})
	require.ErrorIs(t, err, ErrStorageUnavailable)
	require.Zero(t, h.claimCount(t, claimUpload), "the reservation was released")
	h.requireEveryObjectDiscoverable(t)
}
