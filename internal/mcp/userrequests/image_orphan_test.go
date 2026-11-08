package userrequests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// orphanHarness wires the create handler to an isolated SQLite database and a
// recording fake object store, so each test can inject failures (DB triggers,
// store hooks) without disturbing the shared test database.
type orphanHarness struct {
	handler http.Handler
	svc     *Service
	store   *storage.FakeStore
	db      *sql.DB
}

var orphanDBSeq atomic.Int64

// newOrphanHarness builds a harness on a private shared-cache in-memory
// database. dsnParams are appended to the SQLite DSN.
func newOrphanHarness(t *testing.T, settings ImageSettings, dsnParams string) *orphanHarness {
	t.Helper()
	return newOrphanHarnessWithDSN(t, settings,
		fmt.Sprintf("file:userrequests_orphan_%d?mode=memory&cache=shared%s", orphanDBSeq.Add(1), dsnParams))
}

// newOrphanHarnessWithDSN builds a harness on the given SQLite DSN.
func newOrphanHarnessWithDSN(t *testing.T, settings ImageSettings, dsn string) *orphanHarness {
	t.Helper()
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	svc, err := NewService(db, nil, fixedClock(time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)).Now, Settings{
		RetentionDays: DefaultRetentionDays,
		Images:        settings,
	})
	require.NoError(t, err)
	store := storage.NewFakeStore()
	manager := NewImageManager(store, nil, settings)
	return &orphanHarness{
		handler: NewCombinedHTTPHandlerWithImages(svc, nil, manager, log.Logger.Named("test_orphans"), nil),
		svc:     svc,
		store:   store,
		db:      db,
	}
}

// post submits a multipart request carrying files as image parts.
func (h *orphanHarness) post(t *testing.T, ctx context.Context, token string, files ...[]byte) *httptest.ResponseRecorder {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	require.NoError(t, mw.WriteField("content", "see attached"))
	for i, file := range files {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename="img%d.png"`, i))
		hdr.Set("Content-Type", "image/png")
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = part.Write(file)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/requests", buf).WithContext(ctx)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// referencedKeys returns every storage key an image ref row points to.
func (h *orphanHarness) referencedKeys(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := h.db.Query(`SELECT storage_key FROM mcp_user_image_refs`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		out[key] = true
	}
	require.NoError(t, rows.Err())
	return out
}

// requireNoUnreferencedObjects is the issue #87 invariant: every object in the
// store is owned by an image ref row, so it is visible to quota accounting.
func (h *orphanHarness) requireNoUnreferencedObjects(t *testing.T) {
	t.Helper()
	referenced := h.referencedKeys(t)
	for _, key := range h.store.Keys() {
		require.True(t, referenced[key], "object %q is stored but no image ref owns it (orphan outside quota)", key)
	}
}

// errorCode decodes the JSON error code of a failed create.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	code, _ := body["error"].(string)
	return code
}

// putCounter fails the Put whose 1-based ordinal equals failAt.
func putCounter(failAt int64, err error) func(context.Context, string) error {
	var n atomic.Int64
	return func(context.Context, string) error {
		if n.Add(1) == failAt {
			return err
		}
		return nil
	}
}

// TestCreateWithImagesLeavesNoOrphanObjects reproduces issue #87: every
// failure after an object could have been written must leave no object that
// no image ref owns. Each case also checks the fixture was actually reached.
func TestCreateWithImagesLeavesNoOrphanObjects(t *testing.T) {
	first, second := testPNG(t, 40, 30), testPNG(t, 31, 17)
	storeDown := errs.New("injected object store failure")

	t.Run("later attachment fails to decode", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		rec := h.post(t, context.Background(), "sk-orphan-decode-123456", first, []byte("\x89PNG\r\n\x1a\nnot really a png"))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "decode_failed", errorCode(t, rec))
		require.Empty(t, h.store.PutKeys(), "nothing may be uploaded before every attachment is normalized")
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("later put fails", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		h.store.BeforePut = putCounter(2, storeDown)
		rec := h.post(t, context.Background(), "sk-orphan-put2-1234567", first, second)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		require.Len(t, h.store.PutKeys(), 1, "the first object must have been written")
		require.NotEmpty(t, h.store.DeletedKeys(), "compensation must have run")
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("put fails after the server stored the object", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		h.store.AfterPut = putCounter(1, storeDown)
		rec := h.post(t, context.Background(), "sk-orphan-ambig-123456", first)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		require.Len(t, h.store.PutKeys(), 1)
		require.Equal(t, h.store.PutKeys(), h.store.DeletedKeys(), "the ambiguous upload must be deleted")
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("quota rejects the request", func(t *testing.T) {
		settings := defaultImageSettings()
		settings.PerUserQuotaBytes = 64
		h := newOrphanHarness(t, settings, "")
		rec := h.post(t, context.Background(), "sk-orphan-quota-123456", first, second)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "quota_exceeded", errorCode(t, rec))
		require.Empty(t, h.store.PutKeys(), "quota must be reserved before any upload")
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("request insert fails", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		_, err := h.db.Exec(`CREATE TRIGGER fail_request_insert BEFORE INSERT ON mcp_user_requests
			BEGIN SELECT RAISE(ABORT, 'injected request insert failure'); END`)
		require.NoError(t, err)
		rec := h.post(t, context.Background(), "sk-orphan-reqins-12345", first)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "body=%s", rec.Body.String())
		require.Empty(t, h.store.PutKeys())
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("image link insert fails", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		_, err := h.db.Exec(`CREATE TRIGGER fail_link_insert BEFORE INSERT ON mcp_user_request_image_links
			BEGIN SELECT RAISE(ABORT, 'injected link insert failure'); END`)
		require.NoError(t, err)
		rec := h.post(t, context.Background(), "sk-orphan-link-1234567", first, second)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "body=%s", rec.Body.String())
		require.Empty(t, h.store.PutKeys())
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("commit fails", func(t *testing.T) {
		// A deferred foreign key is only checked at COMMIT, so the trigger
		// makes the final commit fail after every object was written.
		h := newOrphanHarness(t, defaultImageSettings(), "&_foreign_keys=1")
		_, err := h.db.Exec(`CREATE TABLE commit_guard_parent (id INTEGER PRIMARY KEY)`)
		require.NoError(t, err)
		_, err = h.db.Exec(`CREATE TABLE commit_guard (parent INTEGER REFERENCES commit_guard_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
		require.NoError(t, err)
		_, err = h.db.Exec(`CREATE TRIGGER fail_on_commit AFTER INSERT ON mcp_user_request_image_links
			BEGIN INSERT INTO commit_guard (parent) VALUES (42); END`)
		require.NoError(t, err)
		rec := h.post(t, context.Background(), "sk-orphan-commit-12345", first, second)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "body=%s", rec.Body.String())
		require.Len(t, h.store.PutKeys(), 2, "both objects must have been written before the commit")
		require.ElementsMatch(t, h.store.PutKeys(), h.store.DeletedKeys(), "compensation must delete both uploads")
		h.requireNoUnreferencedObjects(t)
	})

	t.Run("request context canceled after the put", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.store.AfterPut = func(context.Context, string) error {
			cancel()
			return nil
		}
		rec := h.post(t, ctx, "sk-orphan-cancel-12345", first)
		require.NotEqual(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		require.Len(t, h.store.PutKeys(), 1)
		require.Equal(t, h.store.PutKeys(), h.store.DeletedKeys(), "compensation must survive the canceled request context")
		h.requireNoUnreferencedObjects(t)
	})
}

// TestCreateWithImagesQuotaIsCheckedBeforeUpload pins the documented order
// (proposal §3.4 steps 9-10): a request the quota rejects writes nothing.
func TestCreateWithImagesQuotaIsCheckedBeforeUpload(t *testing.T) {
	settings := defaultImageSettings()
	settings.PerUserQuotaBytes = 64
	h := newOrphanHarness(t, settings, "")
	rec := h.post(t, context.Background(), "sk-quota-first-1234567", testPNG(t, 40, 30))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body=%s", rec.Body.String())
	require.Empty(t, h.store.PutKeys(), "no object may be uploaded before the quota admits it")
}

// TestCreateWithImagesCompensationKeepsSharedObjects guards the dedup case:
// objects are content-addressed per user, so a failed request that re-uploads
// an image an earlier committed request owns must delete only its own new
// objects and never the shared one.
func TestCreateWithImagesCompensationKeepsSharedObjects(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	const token = "sk-orphan-shared-12345"
	shared, fresh := testPNG(t, 40, 30), testPNG(t, 33, 21)

	rec := h.post(t, context.Background(), token, shared)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	sharedKeys := h.store.Keys()
	require.Len(t, sharedKeys, 1)

	// The second request re-uploads the shared image and adds a fresh one;
	// the fresh upload reports failure after the store committed it.
	h.store.AfterPut = putCounter(2, errs.New("injected ambiguous put failure"))
	rec = h.post(t, context.Background(), token, shared, fresh)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.PutKeys(), 3, "the fixture must have re-written the shared object and written the fresh one")

	require.Equal(t, sharedKeys, h.store.Keys(), "only the shared, still-referenced object may remain")
	h.requireNoUnreferencedObjects(t)

	// The earlier request still resolves its image.
	auth, err := askuser.ParseAuthorizationContext("Bearer " + token)
	require.NoError(t, err)
	pending, err := h.svc.ConsumeAllPending(context.Background(), auth, "")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Len(t, pending[0].Images, 1)
	_, ok := h.store.ObjectBytes(pending[0].Images[0].StorageKey)
	require.True(t, ok, "the committed request's object must survive the failed request")
}

// TestCreateWithImagesSuccessKeepsObjects pins the unchanged success path.
func TestCreateWithImagesSuccessKeepsObjects(t *testing.T) {
	h := newOrphanHarness(t, defaultImageSettings(), "")
	rec := h.post(t, context.Background(), "sk-orphan-success-1234", testPNG(t, 40, 30), testPNG(t, 22, 22))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Len(t, h.store.Keys(), 2)
	require.Empty(t, h.store.DeletedKeys())
	h.requireNoUnreferencedObjects(t)
	for key := range h.referencedKeys(t) {
		require.True(t, strings.HasPrefix(key, "mcp/images/"), key)
	}
}
