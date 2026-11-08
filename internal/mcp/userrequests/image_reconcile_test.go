package userrequests

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// reconcileNow is the harness clock (see newOrphanHarness).
var reconcileNow = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

// seedObject stores body under key with the given age relative to reconcileNow.
func seedObject(t *testing.T, h *orphanHarness, key string, age time.Duration) {
	t.Helper()
	require.NoError(t, h.store.Put(context.Background(), key, bytes.NewReader([]byte("png")), 3, "image/png", nil))
	require.True(t, h.store.SetLastModified(key, reconcileNow.Add(-age)))
}

// orphanKey returns a managed-shape key for a synthetic digest.
func orphanKey(user string, digit string) string {
	return "mcp/images/" + user + "/" + strings.Repeat(digit, 64) + ".png"
}

// TestReconcileImageObjectsDeletesSeededOrphans is the issue #87 acceptance
// check "reconcile a seeded orphan and assert bounded-time deletion": an
// unreferenced managed object older than the grace period is deleted in the
// next pass; referenced objects, young objects and foreign keys are kept;
// a young orphan is deleted once it ages past the grace period.
func TestReconcileImageObjectsDeletesSeededOrphans(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanGracePeriod = time.Hour
	h := newOrphanHarness(t, settings, "")

	rec := h.post(t, context.Background(), "sk-reconcile-ref-123456", testPNG(t, 40, 30))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	referenced := h.store.Keys()
	require.Len(t, referenced, 1)
	require.True(t, h.store.SetLastModified(referenced[0], reconcileNow.Add(-48*time.Hour)))

	oldOrphan := orphanKey("user-crashed", "a")
	youngOrphan := orphanKey("user-crashed", "b")
	foreign := "mcp/images/README.txt"
	outside := "other/" + strings.Repeat("c", 64) + ".png"
	seedObject(t, h, oldOrphan, 2*time.Hour)
	seedObject(t, h, youngOrphan, 10*time.Minute)
	seedObject(t, h, foreign, 72*time.Hour)
	seedObject(t, h, outside, 72*time.Hour)

	manager := NewImageManager(h.store, nil, settings)
	result, err := h.svc.ReconcileImageObjects(context.Background(), manager, "")
	require.NoError(t, err)
	require.Equal(t, 4, result.Scanned, "only keys under the image prefix are listed")
	require.Equal(t, 2, result.Candidates, "the referenced and the old orphan objects are old managed keys")
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, result.NextCursor, "the listing fit in one batch")
	require.Equal(t, []string{oldOrphan}, h.store.DeletedKeys())
	require.ElementsMatch(t, []string{referenced[0], youngOrphan, foreign, outside}, h.store.Keys())

	// One grace period later the young orphan is collected too.
	h.svc.clock = func() time.Time { return reconcileNow.Add(time.Hour) }
	result, err = h.svc.ReconcileImageObjects(context.Background(), manager, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.Deleted)
	require.ElementsMatch(t, []string{referenced[0], foreign, outside}, h.store.Keys())
	h.requireNoUnreferencedManagedObjects(t, manager)
}

// TestReconcileImageObjectsResumesAcrossBatches checks the bounded pass: each
// pass lists at most OrphanSweepBatch objects and resumes after the last key,
// so a backlog larger than one batch drains over consecutive passes.
func TestReconcileImageObjectsResumesAcrossBatches(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	for _, digit := range []string{"1", "2", "3", "4", "5"} {
		seedObject(t, h, orphanKey("user-batch", digit), 3*time.Hour)
	}
	manager := NewImageManager(h.store, nil, settings)

	cursor, passes := "", 0
	for {
		result, err := h.svc.ReconcileImageObjects(context.Background(), manager, cursor)
		require.NoError(t, err)
		require.LessOrEqual(t, result.Scanned, 2)
		passes++
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
		require.Less(t, passes, 10, "the cursor must make progress")
	}
	require.Empty(t, h.store.Keys())
	require.Len(t, h.store.DeletedKeys(), 5)
}

// TestReconcileImageObjectsSkipsObjectsOfOpenRequests proves the reconciler
// takes the image scope lock before deleting: while a create transaction is
// between uploading an object and committing its ref, a reconciler pass that
// sees the object as old and unreferenced must not delete it. A file database
// without shared cache lets the unlocked pre-filter read the committed state
// (no ref yet), so only the scope lock can stop the delete.
func TestReconcileImageObjectsSkipsObjectsOfOpenRequests(t *testing.T) {
	settings := defaultImageSettings()
	dsn := "file:" + filepath.Join(t.TempDir(), "reconcile.db") + "?_busy_timeout=200"
	h := newOrphanHarnessWithDSN(t, settings, dsn)
	manager := NewImageManager(h.store, nil, settings)
	// Uploads look old, as for a dedup re-upload of an object whose ref the GC removed.
	h.store.Now = func() time.Time { return reconcileNow.Add(-24 * time.Hour) }

	var (
		reconciled bool
		passErr    error
		passResult ImageReconcileResult
	)
	h.store.AfterPut = func(ctx context.Context, key string) error {
		reconciled = true
		passResult, passErr = h.svc.ReconcileImageObjects(context.WithoutCancel(ctx), manager, "")
		return nil
	}
	rec := h.post(t, context.Background(), "sk-reconcile-open-1234", testPNG(t, 40, 30))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.True(t, reconciled, "the pass must run while the request is open")
	t.Logf("pass during open request: result=%+v err=%v", passResult, passErr)
	require.Zero(t, passResult.Deleted)
	require.Empty(t, h.store.DeletedKeys())
	require.Len(t, h.store.Keys(), 1)
	h.requireNoUnreferencedObjects(t)
}

// TestIsManagedImageKey pins the key shape the reconciler may delete.
func TestIsManagedImageKey(t *testing.T) {
	manager := NewImageManager(nil, nil, ImageSettings{Prefix: "mcp/images"})
	sha := strings.Repeat("0123456789abcdef", 4)
	require.True(t, manager.isManagedImageKey(manager.buildKey("user@example.com", sha)))
	for _, key := range []string{
		"mcp/images/" + sha + ".png",
		"mcp/images/u/" + strings.ToUpper(sha) + ".png",
		"mcp/images/u/" + sha + ".jpg",
		"mcp/images/u/v/" + sha + ".png",
		"mcp/images//" + sha + ".png",
		"mcp/images/u!/" + sha + ".png",
		"mcp/imagesX/u/" + sha + ".png",
		"mcp/images/u/" + sha[:63] + ".png",
	} {
		require.False(t, manager.isManagedImageKey(key), key)
	}
}

// requireNoUnreferencedManagedObjects asserts the reconciler's end state for
// managed keys (foreign keys are intentionally left alone).
func (h *orphanHarness) requireNoUnreferencedManagedObjects(t *testing.T, manager *ImageManager) {
	t.Helper()
	referenced := h.referencedKeys(t)
	for _, key := range h.store.Keys() {
		if manager.isManagedImageKey(key) {
			require.True(t, referenced[key], "managed object %q has no image ref", key)
		}
	}
}
