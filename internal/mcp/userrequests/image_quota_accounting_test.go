package userrequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
)

// putPauses holds the uploads of tagged requests (see withRequestTag) until
// each is released, so a test can stop a request right after its quota
// reservation committed and before it finalizes.
type putPauses struct {
	mu     sync.Mutex
	paused map[string]*putPause
}

// putPause is the pause of one tagged request.
type putPause struct {
	reachedOnce sync.Once
	reached     chan struct{}
	release     chan struct{}
	err         error
}

// newPutPauses returns an empty set of pauses.
func newPutPauses() *putPauses {
	return &putPauses{paused: map[string]*putPause{}}
}

// add pauses every upload of the request tagged tag.
func (p *putPauses) add(tag string) *putPause {
	pause := &putPause{reached: make(chan struct{}), release: make(chan struct{})}
	p.mu.Lock()
	p.paused[tag] = pause
	p.mu.Unlock()
	return pause
}

// hook is a storage.FakeStore BeforePut hook.
func (p *putPauses) hook(ctx context.Context, _ string) error {
	p.mu.Lock()
	pause := p.paused[requestTag(ctx)]
	p.mu.Unlock()
	if pause == nil {
		return nil
	}
	pause.reachedOnce.Do(func() { close(pause.reached) })
	select {
	case <-pause.release:
		return pause.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// proceed lets the paused uploads continue.
func (pause *putPause) proceed() { close(pause.release) }

// fail makes the paused uploads fail with err (as an ambiguous PUT would).
func (pause *putPause) fail(err error) {
	pause.err = err
	close(pause.release)
}

// await waits until the request's upload started, which means its
// reservation committed. finished reports a request that ended instead.
func (pause *putPause) await(t *testing.T, finished <-chan error) {
	t.Helper()
	select {
	case <-pause.reached:
	case err := <-finished:
		t.Fatalf("request ended before its upload started (its reservation was refused?): %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("request did not reach its upload")
	}
}

// createAsync runs CreateRequestWithImages in the background under tag.
func createAsync(h *orphanHarness, tag string, auth *askuser.AuthorizationContext, objects ImageObjectWriter, images ...UploadedImage) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.CreateRequestWithImages(withRequestTag(context.Background(), tag), auth, "see attached", "", images, objects)
		done <- err
	}()
	return done
}

// pausableObjects returns an object writer whose uploads pauses can hold.
func pausableObjects(pauses *putPauses) (ImageObjectWriter, *storage.FakeStore) {
	store := storage.NewFakeStore()
	store.BeforePut = pauses.hook
	return NewImageManager(store, nil, ImageSettings{Enabled: true}), store
}

// TestConcurrentReservationsOfOneImageChargeItOnce is the PR #94 review
// repro "concurrent identical uploads are charged twice": two requests of one
// user that attach the same image while the first is still uploading need
// one content-addressed object, so the quota must charge it once. With a
// quota that fits one copy but not two, the second request was refused with
// quota_exceeded. Both orders of finalization are covered.
func TestConcurrentReservationsOfOneImageChargeItOnce(t *testing.T) {
	for _, tc := range []struct {
		name              string
		firstCommitsFirst bool
	}{
		{name: "second commits while first uploads", firstCommitsFirst: false},
		{name: "first commits while second uploads", firstCommitsFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := defaultImageSettings()
			h := newOrphanHarness(t, settings, "")
			auth := testAuth("imgdup-"+strings.ReplaceAll(tc.name, " ", "-"), "aaaa")
			img := makeUploadedImage(strings.Repeat("d", 64), 600*1024)
			h.svc.settings.Images.PerUserQuotaBytes = img.SizeBytes + img.SizeBytes/2 // one copy fits, two do not

			pauses := newPutPauses()
			objects, store := pausableObjects(pauses)
			first := pauses.add("first")
			firstDone := createAsync(h, "first", auth, objects, img)
			first.await(t, firstDone)

			if tc.firstCommitsFirst {
				second := pauses.add("second")
				secondDone := createAsync(h, "second", auth, objects, img)
				second.await(t, secondDone)
				first.proceed()
				require.NoError(t, <-firstDone)
				second.proceed()
				require.NoError(t, <-secondDone, "the second copy of an image already reserved must not be charged again")
			} else {
				_, err := h.svc.CreateRequestWithImages(withRequestTag(context.Background(), "second"), auth, "again", "", []UploadedImage{img}, objects)
				require.NoError(t, err, "the second copy of an image already reserved must not be charged again")
				first.proceed()
				require.NoError(t, <-firstDone)
			}

			usage, err := h.svc.QuotaUsage(context.Background(), auth)
			require.NoError(t, err)
			require.Equal(t, img.SizeBytes, usage.UsedBytes, "one object, charged once")
			require.Equal(t, int64(1), usage.ObjectCount)
			require.Zero(t, h.claimCount(t, claimUpload), "both reservations were consumed")
			_, ok := store.ObjectBytes(img.StorageKey)
			require.True(t, ok)
		})
	}
}

// TestDeduplicatedReservationKeepsBackingItsImage is the negative control of
// the de-duplication above: the second reservation of an image is free only
// while another reservation or a live ref already charges it. When the first
// request fails and releases its claim, the second reservation must keep the
// image charged, so a third request cannot spend the same bytes.
func TestDeduplicatedReservationKeepsBackingItsImage(t *testing.T) {
	settings := defaultImageSettings()
	h := newOrphanHarness(t, settings, "")
	auth := testAuth("imgdup-backing", "aaaa")
	img := makeUploadedImage(strings.Repeat("e", 64), 600*1024)
	other := makeUploadedImage(strings.Repeat("f", 64), 600*1024)
	h.svc.settings.Images.PerUserQuotaBytes = img.SizeBytes + img.SizeBytes/2 // one image fits, two do not

	pauses := newPutPauses()
	objects, _ := pausableObjects(pauses)
	first, second := pauses.add("first"), pauses.add("second")
	firstDone := createAsync(h, "first", auth, objects, img)
	first.await(t, firstDone)
	secondDone := createAsync(h, "second", auth, objects, img)
	second.await(t, secondDone)

	first.fail(errs.New("injected put failure"))
	require.True(t, errs.Is(<-firstDone, ErrStorageUnavailable))

	_, err := h.svc.CreateRequestWithImages(context.Background(), auth, "third", "", []UploadedImage{other}, objects)
	require.Error(t, err, "the second reservation still holds the first image's bytes")
	require.True(t, errs.Is(err, ErrQuotaExceeded), "got %v", err)

	second.proceed()
	require.NoError(t, <-secondDone)
	usage, err := h.svc.QuotaUsage(context.Background(), auth)
	require.NoError(t, err)
	require.Equal(t, img.SizeBytes, usage.UsedBytes)
	require.LessOrEqual(t, usage.UsedBytes, usage.QuotaBytes)
}

// getQuota calls GET /api/quota on the harness handler.
func (h *orphanHarness) getQuota(t *testing.T, token string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// quotaField returns a numeric field of a quota response, failing when it is
// missing.
func quotaField(t *testing.T, body map[string]any, field string) int64 {
	t.Helper()
	raw, ok := body[field]
	require.True(t, ok, "quota response lacks %q: %v", field, body)
	value, ok := raw.(float64)
	require.True(t, ok, "quota field %q is %T", field, raw)
	return int64(value)
}

// TestQuotaReportMatchesAdmission is the PR #94 review repro "quota
// reporting disagrees with quota enforcement": while a request is uploading,
// its reservation counts toward admission, but GET /api/quota reported only
// committed bytes, so a client saw room for an image the next create refused.
// The report now carries the reservation and the bytes admission would accept
// (available_bytes), computed by the same accounting as admission.
func TestQuotaReportMatchesAdmission(t *testing.T) {
	settings := defaultImageSettings()
	h := newOrphanHarness(t, settings, "")
	const token = "sk-quota-report-matches-1234"
	auth := mustAuth(t, token)
	pending := makeUploadedImage(strings.Repeat("a", 64), 400*1024)
	next := makeUploadedImage(strings.Repeat("b", 64), 300*1024)
	h.svc.settings.Images.PerUserQuotaBytes = pending.SizeBytes + next.SizeBytes - 1

	pauses := newPutPauses()
	objects, _ := pausableObjects(pauses)
	pause := pauses.add("pending")
	done := createAsync(h, "pending", auth, objects, pending)
	pause.await(t, done)

	report := h.getQuota(t, token)
	require.Zero(t, quotaField(t, report, "used_bytes"), "nothing is committed yet")
	require.Equal(t, pending.SizeBytes, quotaField(t, report, "reserved_bytes"))
	available := quotaField(t, report, "available_bytes")
	require.Equal(t, h.svc.settings.Images.PerUserQuotaBytes-pending.SizeBytes, available)

	_, err := h.svc.CreateRequestWithImages(context.Background(), auth, "next", "", []UploadedImage{next}, objects)
	require.Less(t, available, next.SizeBytes)
	require.True(t, errs.Is(err, ErrQuotaExceeded), "admission refuses what the report says does not fit; got %v", err)

	pause.proceed()
	require.NoError(t, <-done)
	report = h.getQuota(t, token)
	require.Equal(t, pending.SizeBytes, quotaField(t, report, "used_bytes"))
	require.Zero(t, quotaField(t, report, "reserved_bytes"))
	require.Equal(t, h.svc.settings.Images.PerUserQuotaBytes-pending.SizeBytes, quotaField(t, report, "available_bytes"))
}

// TestQuotaReportUsesEnforcedDefaults pins that an unset (zero) quota or
// object TTL setting is reported as the default that admission and refs
// actually use. A zero quota was reported as 0, which also hides the image
// composer, while creates were admitted against the 100 MiB default.
func TestQuotaReportUsesEnforcedDefaults(t *testing.T) {
	settings := defaultImageSettings()
	settings.PerUserQuotaBytes = 0
	settings.ObjectTTLDays = 0
	h := newOrphanHarness(t, settings, "")
	report := h.getQuota(t, "sk-quota-report-defaults-1234")
	require.Equal(t, DefaultImagePerUserQuotaBytes, quotaField(t, report, "quota_bytes"))
	require.Equal(t, DefaultImagePerUserQuotaBytes, quotaField(t, report, "available_bytes"))
	require.Equal(t, int64(DefaultImageObjectTTLDays), quotaField(t, report, "ttl_days"))
}

// TestImageLockIDsSerializeQuotaPerUserAcrossPrefixes pins the lock layout:
// the quota lock of a user does not depend on the key prefix (so instances
// with different prefixes serialize one user's admissions), scope locks do
// (object ownership is per key), and the lock list is sorted and
// de-duplicated (one global order, no self-deadlock on a hash collision).
func TestImageLockIDsSerializeQuotaPerUserAcrossPrefixes(t *testing.T) {
	const user = "alice@example.com"
	oldKey := "deploy-a/images/alice@example.com/" + strings.Repeat("a", 64) + ".png"
	newKey := "deploy-b/images/alice@example.com/" + strings.Repeat("a", 64) + ".png"

	oldIDs, newIDs := imageLockIDs([]string{user}, []string{oldKey}), imageLockIDs([]string{user}, []string{newKey})
	require.Contains(t, oldIDs, imageQuotaLockID(user))
	require.Contains(t, newIDs, imageQuotaLockID(user), "both prefixes take the same quota lock")
	require.NotEqual(t, imageScopeLockID("deploy-a/images/alice@example.com"), imageScopeLockID("deploy-b/images/alice@example.com"))
	require.NotEqual(t, imageQuotaLockID(user), imageScopeLockID(user), "quota and scope locks live in different namespaces")

	ids := imageLockIDs([]string{user, user}, []string{oldKey, oldKey, newKey})
	require.Len(t, ids, 3)
	require.IsIncreasing(t, ids)
}

// TestImageClaimsTableGainsSHAColumn covers the additive migration of a
// claims table an earlier build created without sha256: the column is added,
// and the legacy upload claims it holds are still charged (by key, with the
// size they recorded) until they are consumed or expire.
func TestImageClaimsTableGainsSHAColumn(t *testing.T) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:userrequests_claims_migration_%d?mode=memory&cache=shared", orphanDBSeq.Add(1)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE mcp_user_image_claims (
		id UUID PRIMARY KEY,
		owner_id UUID NOT NULL,
		user_identity VARCHAR(255) NOT NULL,
		storage_key TEXT NOT NULL,
		kind VARCHAR(16) NOT NULL,
		size_bytes BIGINT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		expires_at TIMESTAMPTZ NOT NULL
	)`)
	require.NoError(t, err)
	auth := testAuth("imgclaims-migrate", "aaaa")
	for _, key := range []string{"mcp/images/u/a.png", "mcp/images/u/a.png", "mcp/images/u/b.png"} {
		_, err = db.Exec(`INSERT INTO mcp_user_image_claims (id, owner_id, user_identity, storage_key, kind, size_bytes, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, gutils.UUID7Bytes().String(), gutils.UUID7Bytes().String(), auth.UserIdentity, key, claimUpload, 100,
			reconcileNow, reconcileNow.Add(time.Hour))
		require.NoError(t, err)
	}

	svc, err := NewService(db, nil, func() time.Time { return reconcileNow }, Settings{RetentionDays: DefaultRetentionDays, Images: defaultImageSettings()})
	require.NoError(t, err)
	_, err = NewService(db, nil, func() time.Time { return reconcileNow }, Settings{RetentionDays: DefaultRetentionDays, Images: defaultImageSettings()})
	require.NoError(t, err, "the migration is idempotent")

	usage, err := svc.QuotaUsage(context.Background(), auth)
	require.NoError(t, err)
	require.Equal(t, int64(200), usage.ReservedBytes, "legacy claims are charged once per key")

	img := makeUploadedImage(strings.Repeat("c", 64), 300)
	_, err = svc.CreateRequestWithImages(context.Background(), auth, "after migration", "", []UploadedImage{img}, testImageObjects())
	require.NoError(t, err)
	usage, err = svc.QuotaUsage(context.Background(), auth)
	require.NoError(t, err)
	require.Equal(t, int64(300), usage.UsedBytes)
	require.Equal(t, int64(200), usage.ReservedBytes)
}
