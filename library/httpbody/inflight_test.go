package httpbody

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// testPending is a pending bound comfortably above the reads these tests make
// before classification.
const testPending = 64 << 10

// declaredRequest builds a POST whose Content-Length matches body.
func declaredRequest(body []byte) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
}

// streamedRequest builds a POST without a declared length.
func streamedRequest(body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewReader(body)))
	request.ContentLength = -1
	return request
}

// tracked tracks request and assigns it to budget for tenant.
func tracked(t *testing.T, request *http.Request, budget *Inflight, tenant string) (*http.Request, *Charge) {
	t.Helper()
	request, charge := Track(request, testPending)
	require.NoError(t, charge.Assign(budget, tenant))
	return request, charge
}

// TestInflightChargesOnlyBytesRead pins the truthful charge: a declared
// length that has not arrived costs nothing, and mid-body the charge equals
// exactly the bytes read so far, for declared and chunked bodies alike.
func TestInflightChargesOnlyBytesRead(t *testing.T) {
	for name, build := range map[string]func([]byte) *http.Request{"declared": declaredRequest, "chunked": streamedRequest} {
		t.Run(name, func(t *testing.T) {
			budget := NewInflight("test_bytes_read_"+name, 1<<20, 1<<20, 0)
			request, charge := tracked(t, build(bytes.Repeat([]byte("a"), 600<<10)), budget, "")
			require.Zero(t, budget.InUse(), "nothing is charged before the body delivers")

			chunk := make([]byte, 1000)
			_, err := io.ReadFull(request.Body, chunk)
			require.NoError(t, err)
			require.Equal(t, int64(1000), budget.InUse(), "the charge equals the bytes read")
			_, err = io.ReadFull(request.Body, chunk[:337])
			require.NoError(t, err)
			require.Equal(t, int64(1337), budget.InUse(), "the charge equals the bytes read")
			require.Equal(t, int64(1337), charge.Charged())

			_, err = io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, int64(600<<10), budget.InUse(), "a whole body is charged exactly its size")

			charge.Release()
			charge.Release()
			require.Zero(t, budget.InUse(), "release is idempotent")
		})
	}
}

// TestChargePendingBytesMoveOnAssign pins classification: bytes read before
// Assign are pending and charged to no budget; Assign moves them to the
// chosen budget and tenant, and later reads are charged there.
func TestChargePendingBytesMoveOnAssign(t *testing.T) {
	budget := NewInflight("test_assign", 1<<20, 1<<20, 512<<10)
	request, charge := Track(declaredRequest(bytes.Repeat([]byte("p"), 10_000)), testPending)
	stored, ok := ChargeFromContext(request.Context())
	require.True(t, ok)
	require.Same(t, charge, stored, "the handle travels in the request context")

	_, err := io.ReadFull(request.Body, make([]byte, 4096))
	require.NoError(t, err)
	require.Equal(t, int64(4096), charge.Pending())
	require.Zero(t, budget.InUse(), "pending bytes are charged to no budget")

	require.NoError(t, charge.Assign(budget, "tenant-a"))
	require.Zero(t, charge.Pending())
	require.Equal(t, int64(4096), budget.InUse(), "Assign moves the pending bytes")
	require.Equal(t, int64(4096), budget.TenantInUse("tenant-a"))
	require.Error(t, charge.Assign(budget, "tenant-b"), "a request is assigned once")

	_, err = io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, int64(10_000), budget.InUse())
	require.Equal(t, int64(10_000), budget.TenantInUse("tenant-a"))
	charge.Release()
	require.Zero(t, budget.InUse())
	require.Zero(t, budget.TenantInUse("tenant-a"))

	var nilCharge *Charge
	require.NoError(t, nilCharge.Assign(budget, "x"))
	nilCharge.Release()
	_, ok = ChargeFromContext(context.Background())
	require.False(t, ok)
}

// TestChargePendingBoundRefusesUnclassifiedReads pins the pending bound: a
// request that is never classified may read at most maxPending bytes, and the
// refusal is counted.
func TestChargePendingBoundRefusesUnclassifiedReads(t *testing.T) {
	request, charge := Track(streamedRequest(bytes.Repeat([]byte("u"), 8192)), 4096)
	before := testutil.ToFloat64(unclassifiedRefused)
	_, err := io.ReadAll(request.Body)
	require.True(t, IsInflightExhausted(err), "got %v", err)
	require.InDelta(t, before+1, testutil.ToFloat64(unclassifiedRefused), 0)
	require.LessOrEqual(t, charge.Pending(), int64(4096))
	_, err = request.Body.Read(make([]byte, 8))
	require.True(t, IsInflightExhausted(err), "a refused body keeps failing")
	budget := NewInflight("test_pending", 1<<20, 1<<20, 0)
	require.True(t, IsInflightExhausted(charge.Assign(budget, "")), "a refused body cannot be assigned later")
	require.Zero(t, budget.InUse())
	charge.Release()

	small, smallCharge := Track(declaredRequest(bytes.Repeat([]byte("s"), 4096)), 4096)
	data, err := io.ReadAll(small.Body)
	require.NoError(t, err, "exactly the pending bound is still readable")
	require.Len(t, data, 4096)
	smallCharge.Release()
}

// TestInflightSlowSendersDoNotReserveCapacity: bodies that declared a large
// length but sent only a few bytes must not hold the budget, so a fresh
// small body still fits.
func TestInflightSlowSendersDoNotReserveCapacity(t *testing.T) {
	const capacity = 64 << 10
	budget := NewInflight("test_slow_senders", capacity, capacity, 0)
	var charges []*Charge
	for range 32 {
		source, sink := io.Pipe()
		request := httptest.NewRequest(http.MethodPost, "/", source)
		request.ContentLength = capacity
		request, charge := tracked(t, request, budget, "")
		charges = append(charges, charge)
		go func() { _, _ = sink.Write([]byte(`{"jsonrpc"`)); _ = sink.CloseWithError(io.ErrUnexpectedEOF) }()
		_, err := io.ReadFull(request.Body, make([]byte, 10))
		require.NoError(t, err)
	}
	require.Equal(t, int64(32*10), budget.InUse(), "stalled senders are charged only what they sent")

	fresh, freshCharge := tracked(t, declaredRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)), budget, "")
	data, err := io.ReadAll(fresh.Body)
	require.NoError(t, err, "a fresh small body must fit while slow senders stall")
	require.Contains(t, string(data), "tools/list")
	freshCharge.Release()
	for _, charge := range charges {
		charge.Release()
	}
	require.Zero(t, budget.InUse())
}

// TestInflightRefusesReadsBeyondCapacity pins the capacity refusal: a read
// whose bytes do not fit fails with a typed error, keeps failing, is counted,
// and the refused body returns what it was charged.
func TestInflightRefusesReadsBeyondCapacity(t *testing.T) {
	const capacity = 64 << 10
	budget := NewInflight("test_refused", capacity, capacity, 0)
	holder, holderCharge := tracked(t, declaredRequest(bytes.Repeat([]byte("h"), 48<<10)), budget, "")
	_, err := io.ReadAll(holder.Body)
	require.NoError(t, err)

	for name, build := range map[string]func([]byte) *http.Request{"declared": declaredRequest, "chunked": streamedRequest} {
		t.Run(name, func(t *testing.T) {
			large, charge := tracked(t, build(bytes.Repeat([]byte("x"), 32<<10)), budget, "")
			counter := inflightRefused.WithLabelValues("test_refused", InflightRefusedCapacity)
			before := testutil.ToFloat64(counter)
			_, err := io.ReadAll(large.Body)
			require.True(t, IsInflightExhausted(err), "got %v", err)
			require.InDelta(t, before+1, testutil.ToFloat64(counter), 0)
			_, err = large.Body.Read(make([]byte, 8))
			require.True(t, IsInflightExhausted(err), "a refused body keeps failing")
			charge.Release()
			require.Equal(t, int64(48<<10), budget.InUse(), "the refused body returns what it was charged")
		})
	}

	holderCharge.Release()
	require.Zero(t, budget.InUse())
}

// TestInflightTenantShareKeepsOtherTenantsServed pins the per-tenant share:
// one tenant holding its share is refused more, counted as tenant_share, while
// another tenant and tenant-less requests still fit.
func TestInflightTenantShareKeepsOtherTenantsServed(t *testing.T) {
	budget := NewInflight("test_share", 256<<10, 32<<10, 64<<10)
	require.Equal(t, int64(64<<10), budget.TenantShare())

	var holds []*Charge
	for range 2 {
		request, charge := tracked(t, declaredRequest(bytes.Repeat([]byte("a"), 32<<10)), budget, "tenant-a")
		_, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		holds = append(holds, charge)
	}
	require.Equal(t, int64(64<<10), budget.TenantInUse("tenant-a"))

	counter := inflightRefused.WithLabelValues("test_share", InflightRefusedTenantShare)
	before := testutil.ToFloat64(counter)
	third, thirdCharge := tracked(t, declaredRequest([]byte(`{"over":"share"}`)), budget, "tenant-a")
	_, err := io.ReadAll(third.Body)
	require.True(t, IsInflightExhausted(err), "a tenant at its share is refused more, got %v", err)
	require.InDelta(t, before+1, testutil.ToFloat64(counter), 0)
	thirdCharge.Release()

	other, otherCharge := tracked(t, declaredRequest(bytes.Repeat([]byte("b"), 32<<10)), budget, "tenant-b")
	_, err = io.ReadAll(other.Body)
	require.NoError(t, err, "another tenant is not blocked by tenant-a's share")
	otherCharge.Release()

	_, pendingCharge := Track(declaredRequest(nil), testPending)
	require.NoError(t, pendingCharge.Assign(budget, "tenant-a"), "a bodyless request of a tenant at its share is admitted")
	pendingCharge.Release()

	for _, charge := range holds {
		charge.Release()
	}
	require.Zero(t, budget.TenantInUse("tenant-a"))
	require.Zero(t, budget.InUse())
	require.Equal(t, int64(32<<10), NewInflight("test_share", 256<<10, 32<<10, 1).TenantShare(), "a share is never below the per-request cap")
	require.Equal(t, int64(256<<10), NewInflight("test_share", 256<<10, 32<<10, 1<<30).TenantShare(), "a share never exceeds the capacity")
	require.Zero(t, NewInflight("test_share", 256<<10, 32<<10, 0).TenantShare())
}

// TestInflightAssignRefusesWhenPendingBytesDoNotFit: classification itself
// can be refused, which the caller answers with 503 before reading more.
func TestInflightAssignRefusesWhenPendingBytesDoNotFit(t *testing.T) {
	budget := NewInflight("test_assign_refused", 4096, 4096, 0)
	holder, holderCharge := tracked(t, declaredRequest(bytes.Repeat([]byte("h"), 4000)), budget, "")
	_, err := io.ReadAll(holder.Body)
	require.NoError(t, err)

	request, charge := Track(declaredRequest(bytes.Repeat([]byte("q"), 500)), testPending)
	_, err = io.ReadFull(request.Body, make([]byte, 200))
	require.NoError(t, err)
	require.True(t, IsInflightExhausted(charge.Assign(budget, "")))
	_, err = request.Body.Read(make([]byte, 8))
	require.True(t, IsInflightExhausted(err), "a request refused at Assign reads nothing more")
	charge.Release()
	holderCharge.Release()
	require.Zero(t, budget.InUse())
}

// TestInflightBodyAtTheCapFitsAnIdleBudget keeps one body at the per-request
// cap admissible: the capacity is never below the cap.
func TestInflightBodyAtTheCapFitsAnIdleBudget(t *testing.T) {
	const perBody = 300 << 10
	budget := NewInflight("test_cap", 0, perBody, 0)
	require.Equal(t, int64(perBody), budget.Capacity(), "the capacity is raised to the per-request cap")
	request, charge := tracked(t, streamedRequest(bytes.Repeat([]byte("z"), perBody)), budget, "")
	data, err := io.ReadAll(request.Body)
	require.NoError(t, err, "a body at the per-request cap fits an idle budget")
	require.Len(t, data, perBody)
	require.Equal(t, int64(perBody), budget.InUse())
	charge.Release()
	require.Zero(t, budget.InUse())

	require.Equal(t, int64(4096), NewInflight("test_cap", 100, 4096, 0).Capacity(), "a capacity below the cap is raised to it")
}

// TestInflightReleaseOnPanic pins the middleware contract: a deferred release
// returns the charge even when the handler panics mid-body.
func TestInflightReleaseOnPanic(t *testing.T) {
	budget := NewInflight("test_panic", 1000, 1000, 0)
	handler := func(_ http.ResponseWriter, r *http.Request) {
		r, charge := Track(r, testPending)
		defer charge.Release()
		require.NoError(t, charge.Assign(budget, ""))
		_, err := io.ReadFull(r.Body, make([]byte, 300))
		require.NoError(t, err)
		require.Equal(t, int64(300), budget.InUse())
		panic("handler failure")
	}
	require.Panics(t, func() { handler(httptest.NewRecorder(), declaredRequest([]byte(strings.Repeat("p", 700)))) })
	require.Zero(t, budget.InUse(), "a panicking handler must release its charge")
}

// TestInflightConcurrentChargesStayWithinCapacity races many bodies and
// tenants; neither the capacity nor any tenant share is ever overcharged and
// every charge is released.
func TestInflightConcurrentChargesStayWithinCapacity(t *testing.T) {
	const capacity = 640 << 10
	budget := NewInflight("test_race", capacity, 256<<10, 256<<10)
	var (
		wg       sync.WaitGroup
		peak     atomic.Int64
		admitted atomic.Int64
		odd      = make(chan error, 64)
	)
	tenants := []string{"t0", "t1", "t2", ""}
	observe := func() {
		for {
			current, highest := budget.InUse(), peak.Load()
			if current <= highest || peak.CompareAndSwap(highest, current) {
				break
			}
		}
		for _, tenant := range tenants[:3] {
			if held := budget.TenantInUse(tenant); held > budget.TenantShare() {
				odd <- &InflightExhaustedError{Capacity: held}
			}
		}
	}
	for i := range 64 {
		wg.Go(func() {
			size := 128<<10 + i*1024
			request := declaredRequest(bytes.Repeat([]byte("c"), size))
			if i%2 == 1 {
				request = streamedRequest(bytes.Repeat([]byte("c"), size))
			}
			request, charge := Track(request, testPending)
			defer charge.Release()
			if err := charge.Assign(budget, tenants[i%len(tenants)]); err != nil {
				odd <- err
				return
			}
			_, err := io.ReadAll(request.Body)
			observe()
			if err != nil {
				if !IsInflightExhausted(err) {
					odd <- err
				}
				return
			}
			admitted.Add(1)
		})
	}
	wg.Wait()
	close(odd)
	for err := range odd {
		require.NoError(t, err, "only budget refusals may fail a read, and no tenant may exceed its share")
	}
	require.LessOrEqual(t, peak.Load(), int64(capacity), "the budget must never be overcharged")
	require.Positive(t, admitted.Load())
	require.Zero(t, budget.InUse(), "every charge is released")
}

// TestInflightIgnoresBodylessRequests keeps streams and GETs uncharged, also
// on a full budget.
func TestInflightIgnoresBodylessRequests(t *testing.T) {
	budget := NewInflight("test_bodyless", 10, 10, 0)
	full, fullCharge := tracked(t, declaredRequest(bytes.Repeat([]byte("f"), 10)), budget, "")
	_, err := io.ReadAll(full.Body)
	require.NoError(t, err)
	request, charge := Track(httptest.NewRequest(http.MethodGet, "/", nil), testPending)
	require.Equal(t, http.NoBody, request.Body)
	require.NoError(t, charge.Assign(budget, ""), "a bodyless request fits a full budget")
	charge.Release()
	fullCharge.Release()
	require.Zero(t, budget.InUse())
}
