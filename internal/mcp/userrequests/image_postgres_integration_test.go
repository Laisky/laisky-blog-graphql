package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// userRequestsPostgresDSNEnv names a disposable PostgreSQL database for the
// image workflow tests that need real row and advisory locks.
const userRequestsPostgresDSNEnv = "MCP_USER_REQUESTS_TEST_POSTGRES_DSN"

// newPostgresOrphanHarness builds an orphan harness on a fresh schema of the
// PostgreSQL database named by MCP_USER_REQUESTS_TEST_POSTGRES_DSN.
func newPostgresOrphanHarness(t *testing.T, settings ImageSettings) *orphanHarness {
	t.Helper()
	dsn := os.Getenv(userRequestsPostgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s is not set", userRequestsPostgresDSNEnv)
	}
	admin, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("urimg_%d_%d", time.Now().UTC().UnixNano(), orphanDBSeq.Add(1))
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	svc, err := NewService(db, nil, func() time.Time { return reconcileNow }, Settings{RetentionDays: DefaultRetentionDays, Images: settings})
	require.NoError(t, err)
	require.True(t, svc.useDollar)
	store := storage.NewFakeStore()
	manager := NewImageManager(store, nil, settings)
	return &orphanHarness{
		handler: NewCombinedHTTPHandlerWithImages(svc, nil, manager, log.Logger.Named("test_pg_orphans"), nil),
		svc:     svc,
		store:   store,
		db:      db,
	}
}

// TestPostgresImageQuotaHoldsUnderConcurrency proves the image scope lock
// serializes reservations: concurrent requests of one user can never push
// live usage above the quota (each sees the usage the previous one committed).
func TestPostgresImageQuotaHoldsUnderConcurrency(t *testing.T) {
	settings := defaultImageSettings()
	h := newPostgresOrphanHarness(t, settings)
	const token = "sk-pg-quota-race-12345"

	images := make([][]byte, 8)
	for i := range images {
		images[i] = testPNG(t, 20+i, 20)
	}
	// Quota fits exactly the three largest of the eight distinct images, so
	// any three fit and no four do.
	sizes := make([]int64, 0, len(images))
	for _, raw := range images {
		prepared, err := NewImageManager(h.store, nil, settings).Prepare(context.Background(), mustAuth(t, token), attachmentsOf([][]byte{raw}))
		require.NoError(t, err)
		sizes = append(sizes, prepared[0].SizeBytes)
	}
	slices.Sort(sizes)
	n := len(sizes)
	require.Less(t, sizes[n-1]+sizes[n-2]+sizes[n-3], sizes[0]+sizes[1]+sizes[2]+sizes[3], "fixture sizes must make any four exceed the quota")
	h.svc.settings.Images.PerUserQuotaBytes = sizes[n-1] + sizes[n-2] + sizes[n-3]

	var wg sync.WaitGroup
	codes := make([]int, len(images))
	for i := range images {
		wg.Go(func() {
			codes[i] = h.post(t, context.Background(), token, images[i]).Code
		})
	}
	wg.Wait()

	usage, err := h.svc.QuotaUsage(context.Background(), mustAuth(t, token))
	require.NoError(t, err)
	t.Logf("codes=%v used=%d quota=%d", codes, usage.UsedBytes, usage.QuotaBytes)
	require.LessOrEqual(t, usage.UsedBytes, usage.QuotaBytes, "concurrent reservations overran the quota")
	accepted := 0
	for _, code := range codes {
		require.Contains(t, []int{http.StatusOK, http.StatusRequestEntityTooLarge}, code)
		if code == http.StatusOK {
			accepted++
		}
	}
	require.Equal(t, 3, accepted, "exactly three images fit the quota")
	h.requireNoUnreferencedObjects(t)
}

// TestPostgresReconcilerWaitsForOpenRequest is the PostgreSQL counterpart of
// TestReconcileImageObjectsSkipsObjectsOfOpenRequests: the pass blocks on the
// advisory lock until the create transaction commits, then keeps the object.
func TestPostgresReconcilerWaitsForOpenRequest(t *testing.T) {
	settings := defaultImageSettings()
	h := newPostgresOrphanHarness(t, settings)
	manager := NewImageManager(h.store, nil, settings)
	h.store.Now = func() time.Time { return reconcileNow.Add(-24 * time.Hour) }

	var (
		wg         sync.WaitGroup
		passResult ImageReconcileResult
		passErr    error
	)
	h.store.AfterPut = func(ctx context.Context, _ string) error {
		wg.Go(func() {
			passResult, passErr = h.svc.ReconcileImageObjects(context.WithoutCancel(ctx), manager, "")
		})
		// Give the pass time to list the object and reach the lock.
		time.Sleep(300 * time.Millisecond)
		return nil
	}
	rec := h.post(t, context.Background(), "sk-pg-reconcile-123456", testPNG(t, 40, 30))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	wg.Wait()
	require.NoError(t, passErr)
	require.Equal(t, 1, passResult.Candidates)
	require.Zero(t, passResult.Deleted)
	require.Len(t, h.store.Keys(), 1)
	h.requireNoUnreferencedObjects(t)
}

// TestPostgresCompensationRacesCommittingRequest runs failing and succeeding
// requests that upload the same image concurrently: whatever the interleaving,
// the committed request's object must survive the failed one's compensation.
func TestPostgresCompensationRacesCommittingRequest(t *testing.T) {
	settings := defaultImageSettings()
	h := newPostgresOrphanHarness(t, settings)
	const token = "sk-pg-compensate-12345"
	shared := testPNG(t, 40, 30)

	var failNext sync.Mutex
	failing := map[string]bool{}
	h.store.AfterPut = func(ctx context.Context, key string) error {
		failNext.Lock()
		defer failNext.Unlock()
		if failing[requestTag(ctx)] {
			return errs.New("injected ambiguous put failure")
		}
		return nil
	}

	for round := range 10 {
		var wg sync.WaitGroup
		for i := range 4 {
			tag := fmt.Sprintf("%d-%d", round, i)
			failNext.Lock()
			failing[tag] = i%2 == 0
			failNext.Unlock()
			wg.Go(func() {
				_ = h.post(t, withRequestTag(context.Background(), tag), token, shared)
			})
		}
		wg.Wait()

		auth := mustAuth(t, token)
		pending, err := h.svc.ConsumeAllPending(context.Background(), auth, "")
		require.NoError(t, err)
		for _, req := range pending {
			for _, img := range req.Images {
				_, ok := h.store.ObjectBytes(img.StorageKey)
				require.True(t, ok, "round %d: committed request %s lost its object", round, req.ID)
			}
		}
		h.requireNoUnreferencedObjects(t)
	}
}

type requestTagKey struct{}

// withRequestTag labels a request context so store hooks can tell requests apart.
func withRequestTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, requestTagKey{}, tag)
}

// requestTag returns the label set by withRequestTag.
func requestTag(ctx context.Context) string {
	tag, _ := ctx.Value(requestTagKey{}).(string)
	return tag
}

// mustAuth parses the authorization context the handler derives from token.
func mustAuth(t *testing.T, token string) *askuser.AuthorizationContext {
	t.Helper()
	auth, err := askuser.ParseAuthorizationContext("Bearer " + token)
	require.NoError(t, err)
	return auth
}

// attachmentsOf wraps raw images as uploaded attachments.
func attachmentsOf(files [][]byte) []AttachmentInput {
	out := make([]AttachmentInput, 0, len(files))
	for i, file := range files {
		out = append(out, AttachmentInput{FileBytes: file, Filename: fmt.Sprintf("img%d.png", i)})
	}
	return out
}
