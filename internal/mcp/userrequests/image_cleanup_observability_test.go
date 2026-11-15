package userrequests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// The tests in this file pin the throughput and visibility of image object
// cleanup (maintainer review of PR #94, P2): the reconciler drains a backlog
// larger than one batch, every pass and delete is counted, the remaining
// backlog is exported, and a missing lifecycle rule is checked again,
// reinstalled (manage mode) and reported — loudly when neither the lifecycle
// rule nor the reconciler is left to clean up.

// metricValue reads one labelled counter of imageMetrics.objectDeletes.
func deleteCount(path, result string) float64 {
	return testutil.ToFloat64(imageMetrics.objectDeletes.WithLabelValues(path, result))
}

// TestReconcileTickDrainsBacklogBeyondOneBatch: with a batch of 2 and 5 due
// claims, one tick used to process 2 claims per interval, so a backlog that
// grows faster than one batch per hour never drains. A tick now runs passes
// until one is not full.
func TestReconcileTickDrainsBacklogBeyondOneBatch(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	for i := range 5 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-drain-%d-1234567890", i), testPNG(t, 30+i, 9))
	}
	require.Len(t, h.store.Keys(), 5)
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	deletedBefore := deleteCount(deletePathReconciler, deleteResultDeleted)
	okBefore := testutil.ToFloat64(imageMetrics.reconcilePasses.WithLabelValues("ok"))

	result, backlog, err := h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 5, result.Claims)
	require.Equal(t, 5, result.Deleted)
	require.Equal(t, 3, result.Passes, "2 + 2 + 1 claims")
	require.Empty(t, h.store.Keys())
	require.Zero(t, backlog.Due)
	require.Zero(t, testutil.ToFloat64(imageMetrics.dueClaims))
	require.InDelta(t, 5, deleteCount(deletePathReconciler, deleteResultDeleted)-deletedBefore, 0)
	require.InDelta(t, 3, testutil.ToFloat64(imageMetrics.reconcilePasses.WithLabelValues("ok"))-okBefore, 0)
	require.InDelta(t, float64(reconcileNow.Add(imageUploadClaimTTL).Unix()), testutil.ToFloat64(imageMetrics.lastReconcileSuccess), 0)
}

// TestReconcileTickExportsBacklogAndFailures: failing deletes end the tick
// after one pass (no hot loop), are counted, and leave a backlog whose size
// and oldest age are exported; once deletes work again the backlog drains.
func TestReconcileTickExportsBacklogAndFailures(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	for i := range 3 {
		crashAfterUpload(t, h, fmt.Sprintf("sk-backlog-%d-1234567890", i), testPNG(t, 40+i, 9))
	}
	due := reconcileNow.Add(imageUploadClaimTTL)
	h.setClock(due.Add(time.Hour))
	h.store.DeleteErr = errs.New("injected delete failure")
	failedBefore := deleteCount(deletePathReconciler, deleteResultFailed)
	errorBefore := testutil.ToFloat64(imageMetrics.reconcilePasses.WithLabelValues("error"))
	lastSuccess := testutil.ToFloat64(imageMetrics.lastReconcileSuccess)

	result, backlog, err := h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.Error(t, err)
	require.Equal(t, 1, result.Passes, "a failing pass ends the tick")
	require.InDelta(t, 2, deleteCount(deletePathReconciler, deleteResultFailed)-failedBefore, 0)
	require.InDelta(t, 1, testutil.ToFloat64(imageMetrics.reconcilePasses.WithLabelValues("error"))-errorBefore, 0)
	require.InDelta(t, lastSuccess, testutil.ToFloat64(imageMetrics.lastReconcileSuccess), 0, "a failing pass is not a success")
	require.EqualValues(t, 1, backlog.Due, "the claim the failing pass did not reach is still due; the failed keys hold live leases")
	require.Equal(t, time.Hour, backlog.OldestDueAge)
	require.InDelta(t, 1, testutil.ToFloat64(imageMetrics.dueClaims), 0)
	require.InDelta(t, time.Hour.Seconds(), testutil.ToFloat64(imageMetrics.oldestDueClaimAge), 0)

	h.store.DeleteErr = nil
	h.setClock(due.Add(time.Hour + imageDeleteLeaseTTL))
	result, backlog, err = h.svc.ReconcileImageObjectsTick(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 3, result.Deleted)
	require.Zero(t, backlog.Due)
	require.Empty(t, h.store.Keys())
}

// TestCompensationDeletesAreCounted: the deletes of a failed create are
// counted under the compensation path, failures included.
func TestCompensationDeletesAreCounted(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	h.store.AfterPut = putCounter(1, errs.New("injected ambiguous put failure"))
	deletedBefore := deleteCount(deletePathCompensation, deleteResultDeleted)
	rec := h.post(t, context.Background(), "sk-compensation-count-12", testPNG(t, 40, 30))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.InDelta(t, 1, deleteCount(deletePathCompensation, deleteResultDeleted)-deletedBefore, 0)

	h.store.AfterPut = putCounter(1, errs.New("injected ambiguous put failure"))
	h.store.DeleteErr = errs.New("injected delete failure")
	failedBefore := deleteCount(deletePathCompensation, deleteResultFailed)
	rec = h.post(t, context.Background(), "sk-compensation-count-34", testPNG(t, 41, 30))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.InDelta(t, 1, deleteCount(deletePathCompensation, deleteResultFailed)-failedBefore, 0)
}

// observedLogger returns a logger whose entries the test can inspect.
func observedLogger(t *testing.T) (logSDK.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, observed := observer.New(zapcore.DebugLevel)
	logger, err := logSDK.NewWithName("image_lifecycle_test", logSDK.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	return logger, observed
}

// fastLifecycleChecks makes the controller re-check every few milliseconds.
func fastLifecycleChecks(t *testing.T, settings *ImageSettings) {
	t.Helper()
	prev := imageLifecycleFirstRecheck
	imageLifecycleFirstRecheck = 5 * time.Millisecond
	t.Cleanup(func() { imageLifecycleFirstRecheck = prev })
	settings.LifecycleCheckInterval = 5 * time.Millisecond
}

// countMessages counts entries at level whose message contains substr.
func countMessages(observed *observer.ObservedLogs, level zapcore.Level, substr string) int {
	n := 0
	for _, entry := range observed.All() {
		if entry.Level == level && strings.Contains(entry.Message, substr) {
			n++
		}
	}
	return n
}

// startController runs the lifecycle controller of a manager over store
// until the test ends.
func startController(t *testing.T, store *storage.FakeStore, settings ImageSettings) *observer.ObservedLogs {
	t.Helper()
	logger, observed := observedLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	NewImageManager(store, nil, settings).StartLifecycleController(ctx, logger)
	return observed
}

// TestLifecycleControllerRestoresErasedRule reproduces the lost update the
// review describes: the rule this process installed at start is erased by
// another writer of the bucket. The rule used to stay missing until the
// next restart; the controller's periodic check reinstalls it.
func TestLifecycleControllerRestoresErasedRule(t *testing.T) {
	settings := defaultImageSettings()
	fastLifecycleChecks(t, &settings)
	store := storage.NewFakeStore()
	startController(t, store, settings)
	prefix := settings.Prefix + "/"

	require.Eventually(t, func() bool { return store.CheckLifecycle(context.Background(), prefix, 7) == nil }, 2*time.Second, time.Millisecond)
	store.RemoveLifecycle(prefix) // another writer replaced the configuration
	require.Eventually(t, func() bool { return store.CheckLifecycle(context.Background(), prefix, 7) == nil }, 2*time.Second, time.Millisecond,
		"the next check must reinstall the rule")
	require.Eventually(t, func() bool { return testutil.ToFloat64(imageMetrics.lifecycleRulePresent) == 1 }, 2*time.Second, time.Millisecond)
}

// TestLifecycleControllerVerifyModeNeverWrites: with lifecycle_mode verify
// (the rule is owned by IaC), the controller only checks. A missing rule is
// reported once at ERROR — with the "no automatic cleanup" ERROR because the
// reconciler is disabled — and the gauge reads 0 until the rule appears.
func TestLifecycleControllerVerifyModeNeverWrites(t *testing.T) {
	settings := defaultImageSettings()
	settings.LifecycleMode = LifecycleModeVerify
	fastLifecycleChecks(t, &settings)
	store := storage.NewFakeStore()
	observed := startController(t, store, settings)

	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.ErrorLevel, "lifecycle_mode is verify") == 1 &&
			countMessages(observed, zapcore.ErrorLevel, "no automatic cleanup") == 1
	}, 2*time.Second, time.Millisecond)
	require.Zero(t, testutil.ToFloat64(imageMetrics.lifecycleRulePresent))
	time.Sleep(30 * time.Millisecond) //nolint:forbidigo // let several checks run
	require.Zero(t, store.LifecycleWrites(), "verify mode must never write the bucket lifecycle")
	require.Equal(t, 1, countMessages(observed, zapcore.ErrorLevel, "lifecycle_mode is verify"), "reported once per outage")

	require.NoError(t, store.EnsureLifecycle(context.Background(), settings.Prefix+"/", 3)) // IaC adds a stricter rule
	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.InfoLevel, "in place again") == 1 && testutil.ToFloat64(imageMetrics.lifecycleRulePresent) == 1
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, 1, store.LifecycleWrites(), "only the IaC write")
}

// TestLifecycleControllerReportsInstallFailureOnce: a manage-mode install
// that keeps failing (no permission) is reported at ERROR once, not at every
// check; with the reconciler enabled there is no "no automatic cleanup" ERROR.
func TestLifecycleControllerReportsInstallFailureOnce(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepInterval = time.Hour
	fastLifecycleChecks(t, &settings)
	store := storage.NewFakeStore()
	store.LifecycleErr = errs.New("AccessDenied")
	observed := startController(t, store, settings)

	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.DebugLevel, "install image bucket lifecycle rule failed") >= 2
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, 1, countMessages(observed, zapcore.ErrorLevel, "install image bucket lifecycle rule failed"))
	require.Zero(t, countMessages(observed, zapcore.ErrorLevel, "no automatic cleanup"))
	require.Zero(t, testutil.ToFloat64(imageMetrics.lifecycleRulePresent))
	require.InDelta(t, 1, testutil.ToFloat64(imageMetrics.reconcilerEnabled), 0)
}

// TestLifecycleControllerReportsRuleThatKeepsDisappearing: in manage mode a
// rule that is gone again right after every write (another writer owns the
// whole configuration) is a WARN at first and an ERROR once it persists.
func TestLifecycleControllerReportsRuleThatKeepsDisappearing(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepInterval = time.Hour
	fastLifecycleChecks(t, &settings)
	store := storage.NewFakeStore()
	store.LifecycleErr = errs.Wrap(storage.ErrLifecycleRuleMissing, "replaced right after the write")
	observed := startController(t, store, settings)

	require.Eventually(t, func() bool {
		return countMessages(observed, zapcore.ErrorLevel, "keeps disappearing") == 1
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, imageLifecycleMissesBeforeError-1, countMessages(observed, zapcore.WarnLevel, "replaced by another writer"))
}

// TestLifecycleDisabledWithoutReconcilerIsReported: object_ttl_days 0
// disables the lifecycle rule; with the reconciler also disabled nothing
// cleans up image objects, which is an ERROR at start.
func TestLifecycleDisabledWithoutReconcilerIsReported(t *testing.T) {
	settings := defaultImageSettings()
	settings.ObjectTTLDays = 0
	store := storage.NewFakeStore()
	observed := startController(t, store, settings)
	require.Equal(t, 1, countMessages(observed, zapcore.ErrorLevel, "no automatic cleanup"))
	require.Zero(t, store.LifecycleWrites())
}
