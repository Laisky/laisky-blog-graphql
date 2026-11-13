package userrequests

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
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

	db, err := sql.Open("pgx", parsed.String())
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

// TestPostgresCompensationRacesCommittingRequest runs failing and succeeding
// requests that upload the same image concurrently: whatever the interleaving,
// the committed requests' objects must survive the failed ones' cleanup.
func TestPostgresCompensationRacesCommittingRequest(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		settings.MaxConcurrentCreatesPerUser = 4
		h := newPostgresOrphanHarness(t, settings, isolation)
		const token = "sk-pg-compensate-12345"
		shared := testPNG(t, 40, 30)

		var mu sync.Mutex
		failing := map[string]bool{}
		h.store.AfterPut = func(ctx context.Context, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			if failing[requestTag(ctx)] {
				return errs.New("injected ambiguous put failure")
			}
			return nil
		}

		for round := range 10 {
			var wg sync.WaitGroup
			for i := range 4 {
				tag := fmt.Sprintf("%d-%d", round, i)
				mu.Lock()
				failing[tag] = i%2 == 0
				mu.Unlock()
				wg.Go(func() {
					_ = h.post(t, withRequestTag(context.Background(), tag), token, shared)
				})
			}
			wg.Wait()

			pending, err := h.svc.ConsumeAllPending(context.Background(), mustAuth(t, token), "")
			require.NoError(t, err)
			for _, req := range pending {
				for _, img := range req.Images {
					_, ok := h.store.ObjectBytes(img.StorageKey)
					require.True(t, ok, "round %d: committed request %s lost its object", round, req.ID)
				}
			}
			h.requireNoUnreferencedObjects(t)
		}
	})
}

// TestPostgresReconcilerKeepsObjectsOfOpenRequests runs reconciler passes
// over due claims of keys that concurrent requests are uploading and
// committing: no committed request may lose its object.
func TestPostgresReconcilerKeepsObjectsOfOpenRequests(t *testing.T) {
	forEachIsolation(t, func(t *testing.T, isolation string) {
		settings := defaultImageSettings()
		h := newPostgresOrphanHarness(t, settings, isolation)
		files := [][]byte{testPNG(t, 40, 30), testPNG(t, 33, 21), testPNG(t, 25, 25)}
		tokens := []string{"sk-pg-reconcile-000000", "sk-pg-reconcile-000001"}
		// Old crashed uploads of the same images by the same callers leave due
		// claims for exactly the keys the requests below upload.
		for _, token := range tokens {
			for _, file := range files {
				crashAfterUpload(t, h, token, file)
			}
		}
		h.setClock(reconcileNow.Add(imageUploadClaimTTL))

		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = h.svc.ReconcileImageObjects(context.Background(), h.manager())
			}
		})
		for i := range 6 {
			wg.Go(func() {
				_ = h.post(t, context.Background(), tokens[i%2], files[i%len(files)])
			})
		}
		time.Sleep(200 * time.Millisecond)
		close(stop)
		wg.Wait()

		for _, token := range tokens {
			pending, err := h.svc.ConsumeAllPending(context.Background(), mustAuth(t, token), "")
			require.NoError(t, err)
			for _, req := range pending {
				for _, img := range req.Images {
					_, ok := h.store.ObjectBytes(img.StorageKey)
					require.True(t, ok, "committed request %s lost its object to the reconciler", req.ID)
				}
			}
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
// a healthy 300 ms store. The per-user create cap now queues them in process.
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
	var ok200 atomic.Int64
	for i := range 3 * pool {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if h.post(t, ctx, "sk-pg-single-caller-xyz", testPNG(t, 30+i, 20)).Code == http.StatusOK {
				ok200.Add(1)
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
	t.Logf("caller requests ok=%d; worst unrelated wait %v", ok200.Load(), worst)
	require.Less(t, worst, 100*time.Millisecond, "a single identity's concurrency starved an unrelated query")
	require.EqualValues(t, 3*pool, ok200.Load(), "queued requests still complete within their deadline")
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
		require.NoError(t, h.svc.lockImageScopes(context.Background(), holder, []string{images[0].StorageKey}))

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
		require.NoError(t, h.svc.insertImageClaim(context.Background(), holder, gutils.UUID7Bytes(), auth.UserIdentity,
			images[0].StorageKey+".other", claimUpload, images[0].SizeBytes, reconcileNow, reconcileNow.Add(time.Hour)))
		require.NoError(t, holder.Commit())

		err = <-result
		require.Error(t, err, "the waiter must see the holder's committed claim")
		require.True(t, errs.Is(err, ErrQuotaExceeded), "got %v", err)
	})
}
