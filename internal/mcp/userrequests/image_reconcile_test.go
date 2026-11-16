package userrequests

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// reconcileNow is the harness clock (see newOrphanHarness).
var reconcileNow = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

// manager returns an ImageManager over the harness store.
func (h *orphanHarness) manager() *ImageManager {
	return NewImageManager(h.store, nil, defaultImageSettings())
}

// setClock moves the service clock.
func (h *orphanHarness) setClock(at time.Time) {
	h.svc.clock = func() time.Time { return at }
}

// claimCount counts claims of kind (any kind when empty).
func (h *orphanHarness) claimCount(t *testing.T, kind string) int {
	t.Helper()
	query, args := `SELECT COUNT(1) FROM mcp_user_image_claims`, []any{}
	if kind != "" {
		query, args = query+` WHERE kind = ?`, []any{kind}
	}
	var n int
	require.NoError(t, h.db.QueryRow(rebindQuery(query, h.svc.useDollar), args...).Scan(&n))
	return n
}

// crashAfterUpload simulates a create that reserved and uploaded its images
// and then died before finalizing or releasing anything.
func crashAfterUpload(t *testing.T, h *orphanHarness, token string, files ...[]byte) []UploadedImage {
	t.Helper()
	auth := mustAuth(t, token)
	images, err := h.manager().Prepare(context.Background(), auth, attachmentsOf(files))
	require.NoError(t, err)
	owner := gutils.UUID7Bytes()
	require.NoError(t, h.svc.reserveImageUploads(context.Background(), auth, owner, images))
	for _, img := range images {
		require.NoError(t, h.manager().PutImage(context.Background(), auth, img))
	}
	return images
}

// TestReconcileImageObjectsDeletesCrashedUploads is the issue #87 acceptance
// check "reconcile a seeded orphan and assert bounded-time deletion": a create
// that crashed after uploading leaves an upload claim; once it expires, the
// next pass deletes the object and the claim.
func TestReconcileImageObjectsDeletesCrashedUploads(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	images := crashAfterUpload(t, h, "sk-reconcile-crash-12345", testPNG(t, 40, 30))
	require.Len(t, h.store.Keys(), 1)

	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Zero(t, result.Claims, "a live upload claim is not due")
	require.Len(t, h.store.Keys(), 1)

	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	result, err = h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claims)
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, h.store.Keys())
	require.Equal(t, []string{images[0].StorageKey}, h.store.DeletedKeys())
	require.Zero(t, h.claimCount(t, ""), "the claim and the delete lease are gone")
}

// TestReconcileImageObjectsKeepsProtectedKeys: a due claim for a key that a
// committed ref owns, or that a live upload claim of another request holds,
// is dropped without deleting the object.
func TestReconcileImageObjectsKeepsProtectedKeys(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	const token = "sk-reconcile-keep-123456"
	shared, uploading := testPNG(t, 40, 30), testPNG(t, 22, 22)
	crashAfterUpload(t, h, token, shared)
	rec := h.post(t, context.Background(), token, shared)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	crashAfterUpload(t, h, token, uploading)
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	// A newer request is uploading the same image right now.
	auth := mustAuth(t, token)
	live, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{uploading}))
	require.NoError(t, err)
	require.NoError(t, h.svc.reserveImageUploads(context.Background(), auth, gutils.UUID7Bytes(), live))

	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 2, result.Claims)
	require.Zero(t, result.Deleted)
	require.Empty(t, h.store.DeletedKeys())
	require.Len(t, h.store.Keys(), 2)
	require.Equal(t, 1, h.claimCount(t, claimUpload), "only the live claim remains")
}

// TestReconcileImageObjectsRetriesFailedDeletes: a compensation delete that
// fails keeps its lease (so nothing can upload the key meanwhile); once the
// lease expires the reconciler deletes the object.
func TestReconcileImageObjectsRetriesFailedDeletes(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	h.store.AfterPut = putCounter(1, errs.New("injected ambiguous put failure"))
	h.store.DeleteErr = errs.New("injected delete failure")
	rec := h.post(t, context.Background(), "sk-reconcile-retry-1234", testPNG(t, 40, 30))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.Keys(), 1, "the failed delete left the object")
	require.Equal(t, 1, h.claimCount(t, claimDelete), "the lease survives the failed delete")

	h.store.DeleteErr = nil
	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Zero(t, result.Claims, "the lease is not due yet")

	h.setClock(reconcileNow.Add(imageDeleteLeaseTTL))
	result, err = h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, h.store.Keys())
	require.Zero(t, h.claimCount(t, ""))
}

// TestReconcileImageObjectsDeletesObjectsOfExpiredRefs: the GC records an
// orphan claim for every ref it expires, and the reconciler deletes the
// object, so storage converges even without a bucket lifecycle rule.
func TestReconcileImageObjectsDeletesObjectsOfExpiredRefs(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	rec := h.post(t, context.Background(), "sk-reconcile-gc-1234567", testPNG(t, 40, 30))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	h.setClock(reconcileNow.Add(8 * 24 * time.Hour))
	n, err := h.svc.GCExpiredImageRefs(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 1, h.claimCount(t, claimOrphan))

	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, h.store.Keys())
	require.Zero(t, h.claimCount(t, ""))
}

// TestReconcileImageObjectsNeverTouchesUnclaimedObjects: the reconciler works
// only from claims, so objects this database never recorded writing (another
// deployment sharing the bucket, foreign keys under the prefix, objects from
// before this release) are never deleted, however old they are.
func TestReconcileImageObjectsNeverTouchesUnclaimedObjects(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	foreign := []string{
		"mcp/images/user-other-deployment/" + strings.Repeat("a", 64) + ".png",
		"mcp/images/README.txt",
	}
	for _, key := range foreign {
		require.NoError(t, h.store.Put(context.Background(), key, bytes.NewReader([]byte("x")), 1, "image/png", nil))
	}
	h.setClock(reconcileNow.Add(365 * 24 * time.Hour))
	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Zero(t, result.Claims)
	require.ElementsMatch(t, foreign, h.store.Keys())
}

// TestReconcileImageObjectsIsBounded: one pass processes at most
// OrphanSweepBatch due claims; a backlog drains over consecutive passes.
func TestReconcileImageObjectsIsBounded(t *testing.T) {
	settings := defaultImageSettings()
	settings.OrphanSweepBatch = 2
	h := newOrphanHarness(t, settings, "")
	for i := range 5 {
		crashAfterUpload(t, h, "sk-reconcile-batch-12345", testPNG(t, 20+i, 20))
	}
	require.Len(t, h.store.Keys(), 5)
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))

	passes := 0
	for len(h.store.Keys()) > 0 {
		result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
		require.NoError(t, err)
		require.LessOrEqual(t, result.Claims, 2)
		passes++
		require.Less(t, passes, 10, "every pass must make progress")
	}
	require.Equal(t, 3, passes)
	require.Zero(t, h.claimCount(t, ""))
}

// mustAuth parses the authorization context the handler derives from token.
func mustAuth(t *testing.T, token string) *askuser.AuthorizationContext {
	t.Helper()
	auth, err := askuser.ParseAuthorizationContext("Bearer " + token)
	require.NoError(t, err)
	return auth
}
