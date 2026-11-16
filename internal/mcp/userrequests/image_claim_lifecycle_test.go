package userrequests

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// The tests in this file pin the claim-lifecycle invariant of proposal §3.9
// (review of PR #94): every object this deployment may have written stays
// discoverable from the database — through an image ref or a claim — until
// it has been deleted after the last write that could still land. A record
// that disappears earlier leaves an object the claims-driven reconciler can
// never find again.

// discoverableKeys returns every storage key an image ref or a claim names.
func (h *orphanHarness) discoverableKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := h.referencedKeys(t)
	rows, err := h.db.Query(`SELECT storage_key FROM mcp_user_image_claims`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		out[key] = true
	}
	require.NoError(t, rows.Err())
	return out
}

// requireEveryObjectDiscoverable asserts the invariant for the current store.
func (h *orphanHarness) requireEveryObjectDiscoverable(t *testing.T) {
	t.Helper()
	known := h.discoverableKeys(t)
	for _, key := range h.store.Keys() {
		require.True(t, known[key], "object %q is stored but neither a ref nor a claim names it; the reconciler can never delete it", key)
	}
}

// settleAndReconcile moves the clock past every claim deadline and runs the
// GC and one reconciler pass, as the background workers eventually would.
func (h *orphanHarness) settleAndReconcile(t *testing.T, at time.Time) ImageReconcileResult {
	t.Helper()
	h.setClock(at)
	_, err := h.svc.GCExpiredImageRefs(context.Background())
	require.NoError(t, err)
	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	return result
}

// TestGCExpiredImageRefsRecordsOrphansAtomically is the review's first P1:
// the GC deleted refs and links and only then recorded orphan claims in
// separate statements. A failure in between lost the only record of the
// object. Deleting the refs and recording the claims must commit together.
func TestGCExpiredImageRefsRecordsOrphansAtomically(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	rec := h.post(t, context.Background(), "sk-gc-atomic-123456789", testPNG(t, 40, 30))
	require.Equal(t, 200, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.Keys(), 1)

	_, err := h.db.Exec(`CREATE TRIGGER fail_orphan_claim BEFORE INSERT ON mcp_user_image_claims
		WHEN NEW.kind = 'orphan' BEGIN SELECT RAISE(ABORT, 'injected orphan claim failure'); END`)
	require.NoError(t, err)

	expired := reconcileNow.Add(8 * 24 * time.Hour)
	h.setClock(expired)
	_, err = h.svc.GCExpiredImageRefs(context.Background())
	require.Error(t, err, "the injected failure must surface")
	require.Len(t, h.referencedKeys(t), 1, "the ref deletion must roll back with the failed claim insert")
	h.requireEveryObjectDiscoverable(t)

	_, err = h.db.Exec(`DROP TRIGGER fail_orphan_claim`)
	require.NoError(t, err)
	n, err := h.svc.GCExpiredImageRefs(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Empty(t, h.referencedKeys(t))
	h.requireEveryObjectDiscoverable(t)

	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, h.store.Keys())
}

// TestGCExpiredImageRefsDrainsInBatches: each GC transaction is bounded and
// a sweep keeps going until no expired ref is left.
func TestGCExpiredImageRefsDrainsInBatches(t *testing.T) {
	prev := imageGCBatchSize
	imageGCBatchSize = 2
	t.Cleanup(func() { imageGCBatchSize = prev })

	h := newOrphanHarness(t, defaultImageSettings(), "")
	for i := range 5 {
		rec := h.post(t, context.Background(), "sk-gc-batches-12345678", testPNG(t, 20+i, 20))
		require.Equal(t, 200, rec.Code, "body=%s", rec.Body.String())
	}
	h.setClock(reconcileNow.Add(8 * 24 * time.Hour))
	n, err := h.svc.GCExpiredImageRefs(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
	require.Empty(t, h.referencedKeys(t))
	require.Equal(t, 5, h.claimCount(t, claimOrphan))
	var links int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(1) FROM mcp_user_request_image_links`).Scan(&links))
	require.Zero(t, links)
}

// TestCreateWithImagesLatePutAfterCompensationIsReconciled is the review's
// second P1: a PUT the client gave up on may still be applied by the store
// after compensation deleted the key and dropped its lease, recreating an
// object with no ref and no claim. The ambiguous key must keep a record that
// the reconciler re-checks once the late-write window has passed.
func TestCreateWithImagesLatePutAfterCompensationIsReconciled(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	var abandoned []string
	h.store.BeforePut = func(_ context.Context, key string) error {
		abandoned = append(abandoned, key)
		return errs.New("injected client-side PUT timeout; the server is still processing it")
	}
	img := testPNG(t, 40, 30)
	rec := h.post(t, context.Background(), "sk-late-put-1234567890", img)
	require.Equal(t, 503, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, abandoned, 1, "the fixture must have been reached")
	require.Empty(t, h.store.Keys(), "nothing was stored when the request failed")

	// The store applies the abandoned PUT after the request was compensated.
	h.store.BeforePut = nil
	require.NoError(t, h.store.Put(context.Background(), abandoned[0], bytes.NewReader([]byte("late")), 4, "image/png", nil))
	h.requireEveryObjectDiscoverable(t)

	// The record is not due before the late-write window ends.
	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Zero(t, result.Claims)

	result = h.settleAndReconcile(t, reconcileNow.Add(imageLateWriteWindow))
	require.Equal(t, 1, result.Deleted)
	require.Empty(t, h.store.Keys(), "the late write must be removed")
	require.Zero(t, h.claimCount(t, ""))
}

// TestCreateWithImagesLatePutKeepsLaterCommittedObject: the re-check left by
// an ambiguous PUT never deletes an object a later request committed.
func TestCreateWithImagesLatePutKeepsLaterCommittedObject(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	const token = "sk-late-put-keep-123456"
	img := testPNG(t, 40, 30)
	h.store.BeforePut = putCounter(1, errs.New("injected client-side PUT timeout"))
	rec := h.post(t, context.Background(), token, img)
	require.Equal(t, 503, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, h.claimCount(t, claimOrphan), "the ambiguous key keeps a re-check record")

	h.store.BeforePut = nil
	rec = h.post(t, context.Background(), token, img)
	require.Equal(t, 200, rec.Code, "the re-check record must not block a retry; body=%s", rec.Body.String())

	result := h.settleAndReconcile(t, reconcileNow.Add(imageLateWriteWindow))
	require.Zero(t, result.Deleted)
	require.Len(t, h.store.Keys(), 1)
	for key := range h.referencedKeys(t) {
		_, ok := h.store.ObjectBytes(key)
		require.True(t, ok, "committed ref %q lost its object", key)
	}
	require.Zero(t, h.claimCount(t, ""))
}

// scriptedWriter is an ImageObjectWriter whose PutImage runs put; deletes go
// to the harness store.
type scriptedWriter struct {
	h   *orphanHarness
	put func(ctx context.Context, auth *askuser.AuthorizationContext, img UploadedImage) error
}

// PutImage implements ImageObjectWriter.
func (w scriptedWriter) PutImage(ctx context.Context, auth *askuser.AuthorizationContext, img UploadedImage) error {
	return w.put(ctx, auth, img)
}

// DeleteImage implements ImageObjectWriter.
func (w scriptedWriter) DeleteImage(ctx context.Context, key string) error {
	return w.h.manager().DeleteImage(ctx, key)
}

// TestOverlappingFailedCreatesReleaseSharedKey is the review's third P1: A
// and B reserve the same key Y. A uploads Y and fails; its cleanup keeps Y
// because B still claims it. B then fails on X before it touches Y and, when
// cleanup only considered the keys B itself uploaded, released the last
// claim on Y without scheduling its deletion. The last request to release a
// key is responsible for it, whether or not it uploaded the key; A's
// late-write re-check of Y is a second, later line of defense.
func TestOverlappingFailedCreatesReleaseSharedKey(t *testing.T) {
	// One upload at a time, so B fails on X before its upload of Y starts,
	// as it does whenever a request holds more images than the upload
	// concurrency. With Y attempted, cleanup that only considered attempted
	// keys would pass by accident.
	prevConcurrency := imageUploadConcurrency
	imageUploadConcurrency = 1
	t.Cleanup(func() { imageUploadConcurrency = prevConcurrency })
	h := newOrphanHarness(t, defaultImageSettings(), "")
	auth := mustAuth(t, "sk-overlap-release-1234")
	x, y, z := testPNG(t, 41, 30), testPNG(t, 42, 30), testPNG(t, 43, 30)
	imagesB, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{x, y}))
	require.NoError(t, err)
	imagesA, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{y, z}))
	require.NoError(t, err)
	keyY := imagesA[0].StorageKey
	require.Equal(t, keyY, imagesB[1].StorageKey, "both requests must reserve the same key")

	bUploading, failB := make(chan struct{}), make(chan struct{})
	writerB := scriptedWriter{h: h, put: func(_ context.Context, _ *askuser.AuthorizationContext, img UploadedImage) error {
		if img.StorageKey != imagesB[0].StorageKey {
			return errs.New("fixture broken: B must fail before it uploads Y")
		}
		close(bUploading)
		<-failB
		return errs.Wrap(ErrStorageUnavailable, "injected PUT failure of X")
	}}
	writerA := scriptedWriter{h: h, put: func(ctx context.Context, auth *askuser.AuthorizationContext, img UploadedImage) error {
		if img.StorageKey == keyY {
			return h.manager().PutImage(ctx, auth, img)
		}
		return errs.Wrap(ErrStorageUnavailable, "injected PUT failure of Z")
	}}

	var wg sync.WaitGroup
	var errB error
	wg.Go(func() {
		_, errB = h.svc.CreateRequestWithImages(context.Background(), auth, "b", "", imagesB, writerB)
	})
	<-bUploading
	_, errA := h.svc.CreateRequestWithImages(context.Background(), auth, "a", "", imagesA, writerA)
	require.ErrorIs(t, errA, ErrStorageUnavailable)
	_, ok := h.store.ObjectBytes(keyY)
	require.True(t, ok, "Y must survive A's cleanup while B still claims it")

	close(failB)
	wg.Wait()
	require.ErrorIs(t, errB, ErrStorageUnavailable)
	_, ok = h.store.ObjectBytes(keyY)
	require.False(t, ok, "B released the last claim on Y, so B's cleanup must delete it")

	h.requireEveryObjectDiscoverable(t)
	h.settleAndReconcile(t, reconcileNow.Add(imageUploadClaimTTL))
	require.Empty(t, h.store.Keys(), "no object of the two failed requests may remain")
	require.Zero(t, h.claimCount(t, ""))
}

// TestFinalizeRepointedRefRecordsOldKey: when a live ref of the same image is
// re-pointed at a new key (the key prefix changed between deployments), the
// object under the old key loses its only ref and must stay discoverable.
func TestFinalizeRepointedRefRecordsOldKey(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	auth := mustAuth(t, "sk-repoint-ref-12345678")
	img := testPNG(t, 40, 30)

	oldSettings := defaultImageSettings()
	oldSettings.Prefix = "mcp/images-old"
	oldManager := NewImageManager(h.store, nil, oldSettings)
	oldImages, err := oldManager.Prepare(context.Background(), auth, attachmentsOf([][]byte{img}))
	require.NoError(t, err)
	_, err = h.svc.CreateRequestWithImages(context.Background(), auth, "old", "", oldImages, oldManager)
	require.NoError(t, err)

	newImages, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{img}))
	require.NoError(t, err)
	require.NotEqual(t, oldImages[0].StorageKey, newImages[0].StorageKey)
	_, err = h.svc.CreateRequestWithImages(context.Background(), auth, "new", "", newImages, h.manager())
	require.NoError(t, err)
	require.Len(t, h.store.Keys(), 2)
	h.requireEveryObjectDiscoverable(t)

	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, 1, result.Deleted)
	require.Equal(t, []string{newImages[0].StorageKey}, h.store.Keys(), "only the object the ref points at may remain")
}

// TestReconcileImageObjectsDeletesConcurrently: a pass issues its deletes
// with bounded parallelism, so a slow store does not serialize the batch.
func TestReconcileImageObjectsDeletesConcurrently(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	const keys = imageDeleteConcurrency
	for i := range keys {
		crashAfterUpload(t, h, "sk-reconcile-parallel-12", testPNG(t, 20+i, 20))
	}
	var mu sync.Mutex
	inFlight, peak := 0, 0
	allIn := make(chan struct{})
	h.store.BeforeDelete = func(ctx context.Context, _ string) error {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		if inFlight == keys {
			close(allIn)
		}
		mu.Unlock()
		select {
		case <-allIn:
		case <-ctx.Done():
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}
	h.setClock(reconcileNow.Add(imageUploadClaimTTL))
	start := time.Now()
	result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
	require.NoError(t, err)
	require.Equal(t, keys, result.Deleted)
	require.Equal(t, keys, peak, "every delete of the pass must have been in flight at once")
	require.Less(t, time.Since(start), imageObjectDeleteTimeout, "deletes must not wait for each other")
	require.Empty(t, h.store.Keys())
	require.Zero(t, h.claimCount(t, ""))
}

// TestGCKeepsStaleClaimsWhileReconcilerIsEnabled (second review, I2): the
// GC purge of claims that expired more than one object TTL ago only exists to
// bound the table when nothing processes it. With the reconciler enabled a
// stale claim is the only record of its object (for example when the
// lifecycle rule could not be installed), so it must be kept for the next pass.
func TestGCKeepsStaleClaimsWhileReconcilerIsEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
		kept     bool
	}{
		{"reconciler enabled", time.Hour, true},
		{"reconciler disabled", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := defaultImageSettings()
			settings.OrphanSweepInterval = tc.interval
			h := newOrphanHarness(t, settings, "")
			crashAfterUpload(t, h, "sk-gc-stale-claims-1234", testPNG(t, 40, 30))
			require.Equal(t, 1, h.claimCount(t, claimUpload))

			h.setClock(reconcileNow.Add(imageUploadClaimTTL + 8*24*time.Hour))
			_, err := h.svc.GCExpiredImageRefs(context.Background())
			require.NoError(t, err)
			if tc.kept {
				require.Equal(t, 1, h.claimCount(t, claimUpload), "the reconciler still needs the record")
				h.requireEveryObjectDiscoverable(t)
				return
			}
			require.Zero(t, h.claimCount(t, ""))
		})
	}
}
