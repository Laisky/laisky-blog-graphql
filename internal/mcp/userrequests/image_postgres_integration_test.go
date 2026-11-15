package userrequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/storage"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// userRequestsPostgresDSNEnv names a disposable PostgreSQL database for the
// image workflow tests that need real row and advisory locks.
const userRequestsPostgresDSNEnv = "MCP_USER_REQUESTS_TEST_POSTGRES_DSN"

// pgIsolationDefaults are the database default isolation levels the lock
// based tests run under; the workflow must not depend on the default
// (review finding: REPEATABLE READ made the snapshot predate the lock wait).
var pgIsolationDefaults = []string{"read committed", "repeatable read"}

// newPostgresOrphanHarness builds an orphan harness on a fresh schema of the
// PostgreSQL database named by MCP_USER_REQUESTS_TEST_POSTGRES_DSN. A non-empty
// isolation sets default_transaction_isolation for every pooled connection.
func newPostgresOrphanHarness(t *testing.T, settings ImageSettings, isolation string) *orphanHarness {
	t.Helper()
	return newPostgresHarnessOnDSN(t, settings, postgresTestSchemaDSN(t, isolation), isolation)
}

// postgresTestSchemaDSN creates a fresh schema in the PostgreSQL database
// named by MCP_USER_REQUESTS_TEST_POSTGRES_DSN (skipping the test when it is
// unset) and returns a DSN whose connections use it. Harnesses built on the
// same DSN share one database, like instances of one deployment.
func postgresTestSchemaDSN(t *testing.T, isolation string) string {
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
	if isolation != "" {
		query.Set("default_transaction_isolation", isolation)
	}
	// pgx does not decode '+' as a space in DSN parameters.
	parsed.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	return parsed.String()
}

// newPostgresHarnessOnDSN builds an orphan harness with its own connection
// pool, service and image manager on a schema from postgresTestSchemaDSN.
func newPostgresHarnessOnDSN(t *testing.T, settings ImageSettings, dsn, isolation string) *orphanHarness {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	svc, err := NewService(db, nil, func() time.Time { return reconcileNow }, Settings{RetentionDays: DefaultRetentionDays, Images: settings})
	require.NoError(t, err)
	require.True(t, svc.useDollar)
	if isolation != "" {
		var got string
		require.NoError(t, db.QueryRow(`SHOW default_transaction_isolation`).Scan(&got))
		require.Equal(t, isolation, got)
	}
	store := storage.NewFakeStore()
	manager := NewImageManager(store, nil, settings)
	return &orphanHarness{
		handler: NewCombinedHTTPHandlerWithImages(svc, nil, manager, log.Logger.Named("test_pg_orphans"), nil),
		svc:     svc,
		store:   store,
		db:      db,
		images:  manager,
	}
}

// forEachIsolation runs fn once per database default isolation level.
func forEachIsolation(t *testing.T, fn func(t *testing.T, isolation string)) {
	for _, isolation := range pgIsolationDefaults {
		t.Run(isolation, func(t *testing.T) { fn(t, isolation) })
	}
}

// TestPostgresImageQuotaHoldsUnderConcurrency proves the image scope lock
// serializes reservations: concurrent requests of one user can never push
// live usage above the quota (each sees what the previous one reserved).
func TestPostgresImageQuotaHoldsUnderConcurrency(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		settings.MaxConcurrentCreatesPerUser = 8 // let all eight race
		h := newPostgresOrphanHarness(t, settings, isolation)
		const token = "sk-pg-quota-race-12345"

		images := make([][]byte, 8)
		for i := range images {
			images[i] = testPNG(t, 20+i, 20)
		}
		// Quota fits exactly the three largest of the eight distinct images,
		// so any three fit and no four do.
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
	})
}

// awaitUpload waits until a request posted in the background reached its
// upload (its reservation committed); a request that ended first fails the
// test with its response.
func awaitUpload(t *testing.T, pause *putPause, finished <-chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case <-pause.reached:
	case rec := <-finished:
		t.Fatalf("request ended before its upload started: %d %s", rec.Code, rec.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("request did not reach its upload")
	}
}

// postAsync posts files in the background under tag.
func (h *orphanHarness) postAsync(t *testing.T, tag, token string, files ...[]byte) <-chan *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.post(t, withRequestTag(context.Background(), tag), token, files...) }()
	return done
}

// requestIDOf decodes the request id of a successful create.
func requestIDOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Request struct {
			ID string `json:"id"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	require.NotEmpty(t, body.Request.ID)
	return body.Request.ID
}

// requireCommittedWithObjects consumes token's pending requests and requires
// exactly the ids in want, each with one image whose object is stored under
// its key.
func (h *orphanHarness) requireCommittedWithObjects(t *testing.T, token string, want map[string]string) {
	t.Helper()
	pending, err := h.svc.ConsumeAllPending(context.Background(), mustAuth(t, token), "")
	if len(want) == 0 {
		require.True(t, errs.Is(err, ErrNoPendingRequests), "got %v", err)
		return
	}
	require.NoError(t, err)
	got := make(map[string]string, len(pending))
	for _, req := range pending {
		require.Len(t, req.Images, 1, "request %s", req.ID)
		key := req.Images[0].StorageKey
		_, ok := h.store.ObjectBytes(key)
		require.True(t, ok, "committed request %s lost its object %s", req.ID, key)
		got[req.ID.String()] = key
	}
	require.Equal(t, want, got, "exactly the successful creates are committed, each with its image")
}

// TestPostgresCompensationRacesCommittingRequest overlaps failing and
// succeeding requests that upload the same image: every round holds all four
// right after their PUT stored the shared object, so their reservations of the
// shared key provably overlap, then lets the failing ones (an ambiguous PUT:
// stored, then reported failed) compensate before, after or together with the
// committing ones' finalize. The committing requests must succeed with their
// image, and no cleanup may remove the object they reference. (Review of PR
// #94: the earlier version discarded the responses, so it passed even when
// every create failed.)
func TestPostgresCompensationRacesCommittingRequest(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		settings.MaxConcurrentCreatesPerUser = 4
		h := newPostgresOrphanHarness(t, settings, isolation)
		const token = "sk-pg-compensate-12345"
		shared := testPNG(t, 40, 30)
		prepared, err := h.manager().Prepare(context.Background(), mustAuth(t, token), attachmentsOf([][]byte{shared}))
		require.NoError(t, err)
		sharedKey := prepared[0].StorageKey

		pauses := newPutPauses()
		h.store.AfterPut = pauses.hook // the object is stored; the request waits before it learns the outcome
		failing := func(i int) bool { return i%2 == 0 }

		orders := []string{"failures first", "commits first", "together"}
		for round := range 2 * len(orders) {
			order := orders[round%len(orders)]
			paused := make([]*putPause, 4)
			results := make([]<-chan *httptest.ResponseRecorder, 4)
			for i := range 4 {
				tag := fmt.Sprintf("%d-%d", round, i)
				paused[i] = pauses.add(tag)
				results[i] = h.postAsync(t, tag, token, shared)
			}
			for i := range 4 {
				awaitUpload(t, paused[i], results[i])
			}
			require.Equal(t, 4, h.claimCount(t, claimUpload), "round %d: all four reservations are live at once", round)

			committed := map[string]string{}
			release := func(i int) {
				if failing(i) {
					paused[i].fail(errs.New("injected ambiguous put failure"))
					return
				}
				paused[i].proceed()
			}
			collect := func(i int) {
				rec := <-results[i]
				if failing(i) {
					require.Equal(t, http.StatusServiceUnavailable, rec.Code, "round %d (%s): %s", round, order, rec.Body.String())
					require.Equal(t, "storage_unavailable", errorCode(t, rec))
					return
				}
				require.Equal(t, http.StatusOK, rec.Code, "round %d (%s): %s", round, order, rec.Body.String())
				committed[requestIDOf(t, rec)] = sharedKey
			}
			group := func(wantFailing bool) []int {
				var out []int
				for i := range 4 {
					if failing(i) == wantFailing {
						out = append(out, i)
					}
				}
				return out
			}
			var phases [][]int
			switch order {
			case "failures first":
				phases = [][]int{group(true), group(false)}
			case "commits first":
				phases = [][]int{group(false), group(true)}
			default:
				phases = [][]int{{0, 1, 2, 3}}
			}
			for _, phase := range phases {
				for _, i := range phase {
					release(i)
				}
				for _, i := range phase {
					collect(i)
				}
			}
			require.Len(t, committed, 2, "round %d: both committing requests succeeded", round)
			h.requireCommittedWithObjects(t, token, committed)
			h.requireNoUnreferencedObjects(t)
			require.Zero(t, h.claimCount(t, claimUpload), "round %d: every reservation was consumed or released", round)
		}
	})
}

// TestPostgresReconcilerKeepsObjectsOfOpenRequests runs the reconciler over
// due claims of the very keys concurrent requests are uploading, in the two
// orders that matter, each forced rather than left to timing:
//
//   - reservations first: every request holds a live upload claim while a
//     reconciler pass processes the due claims of its key; the pass must
//     process them, delete nothing, and every request must then commit with
//     its object;
//   - lease first: a pass leases a key and is held in its DELETE; a request
//     for that key is refused with image_busy (and uploads nothing), and the
//     same request succeeds once the delete finished.
//
// (Review of PR #94: the earlier version discarded the responses and
// accepted an empty committed set, so it could pass vacuously.)
func TestPostgresReconcilerKeepsObjectsOfOpenRequests(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		settings.MaxConcurrentCreatesPerUser = 3 // every request of a caller is admitted at once
		h := newPostgresOrphanHarness(t, settings, isolation)
		files := [][]byte{testPNG(t, 40, 30), testPNG(t, 33, 21), testPNG(t, 25, 25)}
		tokens := []string{"sk-pg-reconcile-000000", "sk-pg-reconcile-000001"}

		// Reservations first. Old crashed uploads of the same images by the
		// same callers leave due claims for exactly the keys requested below.
		for _, token := range tokens {
			for _, file := range files {
				crashAfterUpload(t, h, token, file)
			}
		}
		h.setClock(reconcileNow.Add(imageUploadClaimTTL))
		pauses := newPutPauses()
		h.store.BeforePut = pauses.hook
		paused := make([]*putPause, 6)
		results := make([]<-chan *httptest.ResponseRecorder, 6)
		for i := range 6 {
			tag := fmt.Sprintf("open-%d", i)
			paused[i] = pauses.add(tag)
			results[i] = h.postAsync(t, tag, tokens[i%2], files[i%len(files)])
		}
		for i := range 6 {
			awaitUpload(t, paused[i], results[i])
		}
		result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
		require.NoError(t, err)
		require.Equal(t, 6, result.Claims, "the pass processed the due claims of the keys being uploaded")
		require.Zero(t, result.Deleted, "a live reservation protects its key")
		require.Len(t, h.store.Keys(), 6)
		want := map[string]map[string]string{tokens[0]: {}, tokens[1]: {}}
		for i := range 6 {
			paused[i].proceed()
		}
		for i := range 6 {
			rec := <-results[i]
			require.Equal(t, http.StatusOK, rec.Code, "request %d: %s", i, rec.Body.String())
			images, err := h.manager().Prepare(context.Background(), mustAuth(t, tokens[i%2]), attachmentsOf([][]byte{files[i%len(files)]}))
			require.NoError(t, err)
			want[tokens[i%2]][requestIDOf(t, rec)] = images[0].StorageKey
		}
		for _, token := range tokens {
			h.requireCommittedWithObjects(t, token, want[token])
		}

		// Lease first. A crashed upload of a new image leaves a due claim; a
		// pass leases its key and is held in the DELETE.
		h.store.BeforePut = nil
		late := testPNG(t, 27, 19)
		crashAfterUpload(t, h, tokens[0], late)
		h.setClock(reconcileNow.Add(2 * imageUploadClaimTTL))
		deleting, releaseDelete := make(chan struct{}), make(chan struct{})
		var deletingOnce sync.Once
		h.store.BeforeDelete = func(ctx context.Context, _ string) error {
			deletingOnce.Do(func() { close(deleting) })
			select {
			case <-releaseDelete:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		passDone := make(chan ImageReconcileResult, 1)
		go func() {
			result, err := h.svc.ReconcileImageObjects(context.Background(), h.manager())
			require.NoError(t, err)
			passDone <- result
		}()
		select {
		case <-deleting:
		case <-time.After(10 * time.Second):
			t.Fatal("the reconciler did not reach the delete")
		}
		rec := h.post(t, context.Background(), tokens[0], late)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "image_busy", errorCode(t, rec), "a key under a delete lease is not reserved")
		close(releaseDelete)
		require.Equal(t, 1, (<-passDone).Deleted)
		h.store.BeforeDelete = nil

		rec = h.post(t, context.Background(), tokens[0], late)
		require.Equal(t, http.StatusOK, rec.Code, "the retry after the delete succeeds: %s", rec.Body.String())
		images, err := h.manager().Prepare(context.Background(), mustAuth(t, tokens[0]), attachmentsOf([][]byte{late}))
		require.NoError(t, err)
		h.requireCommittedWithObjects(t, tokens[0], map[string]string{requestIDOf(t, rec): images[0].StorageKey})
		h.requireNoUnreferencedObjects(t)
	})
}

// slowUploadClaimInserts makes every upload-claim insert of the harness
// schema sleep for delay, which widens the window between a reservation's
// quota check and its commit so concurrent reservations reliably overlap.
func slowUploadClaimInserts(t *testing.T, db *sql.DB, delay time.Duration) {
	t.Helper()
	_, err := db.Exec(fmt.Sprintf(`CREATE FUNCTION slow_upload_claim() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.kind = 'upload' THEN PERFORM pg_sleep(%f); END IF;
			RETURN NEW;
		END $$`, delay.Seconds()))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER slow_upload_claim BEFORE INSERT ON mcp_user_image_claims
		FOR EACH ROW EXECUTE FUNCTION slow_upload_claim()`)
	require.NoError(t, err)
}

// TestPostgresQuotaHoldsAcrossStoragePrefixes is the PR #94 review repro
// "per-user quota locks are tied to the object prefix": two instances of one
// deployment share the database but, during a rolling configuration change,
// use different key prefixes. Their reservations for one user took different
// advisory locks (the lock scope was the key directory), so both saw the
// quota free and together overran it. The quota is now serialized per user
// identity, independently of the key prefix.
func TestPostgresQuotaHoldsAcrossStoragePrefixes(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		dsn := postgresTestSchemaDSN(t, isolation)
		oldSettings, newSettings := defaultImageSettings(), defaultImageSettings()
		oldSettings.Prefix, newSettings.Prefix = "deploy-a/images", "deploy-b/images"
		oldInstance := newPostgresHarnessOnDSN(t, oldSettings, dsn, isolation)
		newInstance := newPostgresHarnessOnDSN(t, newSettings, dsn, isolation)
		slowUploadClaimInserts(t, oldInstance.db, 300*time.Millisecond)
		const token = "sk-pg-prefix-quota-12345"
		auth := mustAuth(t, token)

		for round := range 2 {
			files := [][]byte{testPNG(t, 40+round, 30), testPNG(t, 30, 40+round)}
			sizes := make([]int64, len(files))
			for i, file := range files {
				prepared, err := oldInstance.images.Prepare(context.Background(), auth, attachmentsOf([][]byte{file}))
				require.NoError(t, err)
				sizes[i] = prepared[0].SizeBytes
			}
			usage, err := oldInstance.svc.QuotaUsage(context.Background(), auth)
			require.NoError(t, err)
			// One more image fits, two do not.
			quota := usage.UsedBytes + max(sizes[0], sizes[1]) + min(sizes[0], sizes[1])/2
			oldInstance.svc.settings.Images.PerUserQuotaBytes = quota
			newInstance.svc.settings.Images.PerUserQuotaBytes = quota

			start := make(chan struct{})
			codes := make([]int, 2)
			var wg sync.WaitGroup
			for i, instance := range []*orphanHarness{oldInstance, newInstance} {
				wg.Go(func() {
					<-start
					codes[i] = instance.post(t, context.Background(), token, files[i]).Code
				})
			}
			close(start)
			wg.Wait()

			usage, err = newInstance.svc.QuotaUsage(context.Background(), auth)
			require.NoError(t, err)
			t.Logf("round %d: codes=%v used=%d quota=%d", round, codes, usage.UsedBytes, quota)
			require.LessOrEqual(t, usage.UsedBytes, quota, "reservations under two prefixes overran the quota")
			slices.Sort(codes)
			require.Equal(t, []int{http.StatusOK, http.StatusRequestEntityTooLarge}, codes, "exactly one of the two images fits")
		}
	})
}

// poolProbe measures how long an unrelated query waits for a pooled
// connection while image requests run.
func poolProbe(t *testing.T, db *sql.DB, timeout time.Duration) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
	return time.Since(start), err
}

// TestPostgresStalledStoreDoesNotPinPool is the review repro of pool
// starvation: with the database pool scaled down to 8 connections and the
// object store hanging, 8 image requests used to pin every connection (each
// held an open transaction across its PUT) and an unrelated query timed out.
// Uploads now run outside any transaction.
func TestPostgresStalledStoreDoesNotPinPool(t *testing.T) {
	settings := defaultImageSettings()
	h := newPostgresOrphanHarness(t, settings, "")
	const pool = 8
	h.db.SetMaxOpenConns(pool)
	h.store.BeforePut = func(ctx context.Context, _ string) error {
		<-ctx.Done() // a PUT that hangs until its deadline
		return ctx.Err()
	}

	var wg sync.WaitGroup
	var maxInUse atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if v := int64(h.db.Stats().InUse); v > maxInUse.Load() {
				maxInUse.Store(v)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for i := range pool {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = h.post(t, ctx, fmt.Sprintf("sk-pg-pool-user-%02d-xyz", i), testPNG(t, 30+i, 20))
		})
	}
	time.Sleep(500 * time.Millisecond)
	wait, probeErr := poolProbe(t, h.db, time.Second)
	wg.Wait()
	close(stop)
	t.Logf("unrelated SELECT 1 waited %v (err=%v); max pool InUse=%d of %d", wait, probeErr, maxInUse.Load(), pool)
	require.NoError(t, probeErr, "image requests pinned the pool while the store stalled")
	require.Less(t, wait, 100*time.Millisecond)
	h.requireNoUnreferencedObjects(t)
	require.Zero(t, h.claimCount(t, claimUpload), "every failed request released its reservation")
}

// TestPostgresSameUserConcurrencyDoesNotPinPool is the second review repro:
// 24 concurrent requests of one identity used to queue on the advisory lock,
// each holding a pooled connection, and starve an unrelated query even with
// a healthy 300 ms store. The per-user create cap now admits two at a time,
// queues two more in process, and refuses the rest at once with image_busy
// (the bounded queue of the PR #94 review).
func TestPostgresSameUserConcurrencyDoesNotPinPool(t *testing.T) {
	settings := defaultImageSettings()
	h := newPostgresOrphanHarness(t, settings, "")
	const pool = 8
	h.db.SetMaxOpenConns(pool)
	h.store.BeforePut = func(ctx context.Context, _ string) error {
		select {
		case <-time.After(300 * time.Millisecond): // a multi-MB PUT
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	var ok200, busy, other atomic.Int64
	for i := range 3 * pool {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rec := h.post(t, ctx, "sk-pg-single-caller-xyz", testPNG(t, 30+i, 20))
			switch {
			case rec.Code == http.StatusOK:
				ok200.Add(1)
			case rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), `"image_busy"`):
				busy.Add(1)
			default:
				other.Add(1)
			}
		})
	}
	time.Sleep(300 * time.Millisecond)
	var worst time.Duration
	for range 5 {
		wait, err := poolProbe(t, h.db, 2*time.Second)
		require.NoError(t, err)
		worst = max(worst, wait)
		time.Sleep(200 * time.Millisecond)
	}
	wg.Wait()
	t.Logf("caller requests ok=%d busy=%d; worst unrelated wait %v", ok200.Load(), busy.Load(), worst)
	require.Less(t, worst, 100*time.Millisecond, "a single identity's concurrency starved an unrelated query")
	require.Zero(t, other.Load(), "every request either succeeded or was refused as image_busy")
	require.GreaterOrEqual(t, ok200.Load(), int64(2*DefaultImageMaxConcurrentCreatesPerUser), "admitted and queued requests complete")
	require.Positive(t, busy.Load(), "requests beyond the bounded queue are refused at once")
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

// attachmentsOf wraps raw images as uploaded attachments.
func attachmentsOf(files [][]byte) []AttachmentInput {
	out := make([]AttachmentInput, 0, len(files))
	for i, file := range files {
		out = append(out, AttachmentInput{FileBytes: file, Filename: fmt.Sprintf("img%d.png", i)})
	}
	return out
}

// TestPostgresReservationSeesPriorLockHolder pins why every image
// transaction is READ COMMITTED: a reservation that waited for the scope lock
// must see what the previous holder committed. Under a REPEATABLE READ
// database default the snapshot is taken by the lock statement itself, before
// the wait, so the waiter would miss the holder's claim and overrun the quota.
func TestPostgresReservationSeesPriorLockHolder(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		h := newPostgresOrphanHarness(t, settings, isolation)
		auth := mustAuth(t, "sk-pg-lock-snapshot-1234")
		images, err := h.manager().Prepare(context.Background(), auth, attachmentsOf([][]byte{testPNG(t, 40, 30)}))
		require.NoError(t, err)
		h.svc.settings.Images.PerUserQuotaBytes = images[0].SizeBytes + images[0].SizeBytes/2

		holder, err := h.db.BeginTx(context.Background(), nil)
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		require.NoError(t, h.svc.lockImageQuotaAndScopes(context.Background(), holder, auth.UserIdentity, []string{images[0].StorageKey}))

		result := make(chan error, 1)
		go func() {
			result <- h.svc.reserveImageUploads(context.Background(), auth, gutils.UUID7Bytes(), images)
		}()
		require.Eventually(t, func() bool {
			var waiting int
			_ = h.db.QueryRow(`SELECT COUNT(1) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting)
			return waiting > 0
		}, 5*time.Second, 10*time.Millisecond, "the reservation must be waiting for the lock")

		// The holder reserves the same quota and commits while the other waits.
		require.NoError(t, h.svc.insertImageClaimFor(context.Background(), holder, gutils.UUID7Bytes(), auth.UserIdentity,
			images[0].StorageKey+".other", claimUpload, strings.Repeat("0", 64), images[0].SizeBytes, reconcileNow, reconcileNow.Add(time.Hour)))
		require.NoError(t, holder.Commit())

		err = <-result
		require.Error(t, err, "the waiter must see the holder's committed claim")
		require.True(t, errs.Is(err, ErrQuotaExceeded), "got %v", err)
	})
}

// TestPostgresImageClaimsTableGainsSHAColumn is the PostgreSQL side of
// TestImageClaimsTableGainsSHAColumn: a claims table an earlier build created
// without sha256 is migrated in place and its legacy claims stay charged.
func TestPostgresImageClaimsTableGainsSHAColumn(t *testing.T) {
	dsn := postgresTestSchemaDSN(t, "")
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
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
	auth := mustAuth(t, "sk-pg-claims-migrate-1234")
	_, err = db.Exec(`INSERT INTO mcp_user_image_claims (id, owner_id, user_identity, storage_key, kind, size_bytes, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, gutils.UUID7Bytes().String(), gutils.UUID7Bytes().String(), auth.UserIdentity,
		"mcp/images/u/legacy.png", claimUpload, 100, reconcileNow, reconcileNow.Add(time.Hour))
	require.NoError(t, err)

	h := newPostgresHarnessOnDSN(t, defaultImageSettings(), dsn, "")
	h2 := newPostgresHarnessOnDSN(t, defaultImageSettings(), dsn, "")
	usage, err := h2.svc.QuotaUsage(context.Background(), auth)
	require.NoError(t, err)
	require.Equal(t, int64(100), usage.ReservedBytes, "the legacy claim is still charged")

	rec := h.post(t, context.Background(), "sk-pg-claims-migrate-1234", testPNG(t, 40, 30))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	usage, err = h.svc.QuotaUsage(context.Background(), auth)
	require.NoError(t, err)
	require.Positive(t, usage.UsedBytes)
	require.Equal(t, int64(100), usage.ReservedBytes)
}
