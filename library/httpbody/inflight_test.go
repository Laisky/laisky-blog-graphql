package httpbody

import (
	"bytes"
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

// TestInflightChargesOnlyBytesRead pins the truthful charge: a declared
// length that has not arrived costs nothing, and mid-body the charge equals
// exactly the bytes read so far, for declared and chunked bodies alike.
func TestInflightChargesOnlyBytesRead(t *testing.T) {
	for name, build := range map[string]func([]byte) *http.Request{"declared": declaredRequest, "chunked": streamedRequest} {
		t.Run(name, func(t *testing.T) {
			budget := NewInflight("test_bytes_read_"+name, 1<<20, 1<<20)
			request := build(bytes.Repeat([]byte("a"), 600<<10))
			release := budget.Admit(request)
			require.Zero(t, budget.InUse(), "nothing is charged before the body delivers")

			chunk := make([]byte, 1000)
			_, err := io.ReadFull(request.Body, chunk)
			require.NoError(t, err)
			require.Equal(t, int64(1000), budget.InUse(), "the charge equals the bytes read")
			_, err = io.ReadFull(request.Body, chunk[:337])
			require.NoError(t, err)
			require.Equal(t, int64(1337), budget.InUse(), "the charge equals the bytes read")

			rest, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, int64(1337+len(rest)), budget.InUse())
			require.Equal(t, int64(600<<10), budget.InUse(), "a whole body is charged exactly its size")

			release()
			release()
			require.Zero(t, budget.InUse(), "release is idempotent")
		})
	}
}

// TestInflightSlowSendersDoNotReserveCapacity reproduces the coordinator's
// review of the first design: bodies that declared a large length but sent
// only a few bytes must not hold the budget, so a fresh small body still fits.
func TestInflightSlowSendersDoNotReserveCapacity(t *testing.T) {
	const capacity = 64 << 10
	budget := NewInflight("test_slow_senders", capacity, capacity)
	var releases []func()
	for range 32 {
		source, sink := io.Pipe()
		request := httptest.NewRequest(http.MethodPost, "/", source)
		request.ContentLength = capacity
		releases = append(releases, budget.Admit(request))
		go func() { _, _ = sink.Write([]byte(`{"jsonrpc"`)); _ = sink.CloseWithError(io.ErrUnexpectedEOF) }()
		_, err := io.ReadFull(request.Body, make([]byte, 10))
		require.NoError(t, err)
	}
	require.Equal(t, int64(32*10), budget.InUse(), "stalled senders are charged only what they sent")

	fresh := declaredRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	releaseFresh := budget.Admit(fresh)
	data, err := io.ReadAll(fresh.Body)
	require.NoError(t, err, "a fresh small body must fit while slow senders stall")
	require.Contains(t, string(data), "tools/list")
	releaseFresh()
	for _, release := range releases {
		release()
	}
	require.Zero(t, budget.InUse())
}

// TestInflightRefusesReadsBeyondCapacity pins the refusal: a read whose
// bytes do not fit fails with a typed error, keeps failing, is counted, and
// the refused body returns what it was charged.
func TestInflightRefusesReadsBeyondCapacity(t *testing.T) {
	const capacity = 64 << 10
	budget := NewInflight("test_refused", capacity, capacity)
	holder := declaredRequest(bytes.Repeat([]byte("h"), 48<<10))
	releaseHolder := budget.Admit(holder)
	_, err := io.ReadAll(holder.Body)
	require.NoError(t, err)

	for name, build := range map[string]func([]byte) *http.Request{"declared": declaredRequest, "chunked": streamedRequest} {
		t.Run(name, func(t *testing.T) {
			large := build(bytes.Repeat([]byte("x"), 32<<10))
			release := budget.Admit(large)
			before := testutil.ToFloat64(inflightRefused.WithLabelValues("test_refused"))
			_, err := io.ReadAll(large.Body)
			require.True(t, IsInflightExhausted(err), "got %v", err)
			require.InDelta(t, before+1, testutil.ToFloat64(inflightRefused.WithLabelValues("test_refused")), 0)
			_, err = large.Body.Read(make([]byte, 8))
			require.True(t, IsInflightExhausted(err), "a refused body keeps failing")
			release()
			require.Equal(t, int64(48<<10), budget.InUse(), "the refused body returns what it was charged")
		})
	}

	releaseHolder()
	require.Zero(t, budget.InUse())
}

// TestInflightBodyAtTheCapFitsAnIdleBudget keeps one body at the per-request
// cap admissible: the capacity is never below the cap.
func TestInflightBodyAtTheCapFitsAnIdleBudget(t *testing.T) {
	const perBody = 300 << 10
	budget := NewInflight("test_cap", 0, perBody)
	require.Equal(t, int64(perBody), budget.Capacity(), "the capacity is raised to the per-request cap")
	request := streamedRequest(bytes.Repeat([]byte("z"), perBody))
	release := budget.Admit(request)
	data, err := io.ReadAll(request.Body)
	require.NoError(t, err, "a body at the per-request cap fits an idle budget")
	require.Len(t, data, perBody)
	require.Equal(t, int64(perBody), budget.InUse())
	release()
	require.Zero(t, budget.InUse())

	require.Equal(t, int64(4096), NewInflight("test_cap", 100, 4096).Capacity(), "a capacity below the cap is raised to it")
}

// TestInflightReleaseOnPanic pins the middleware contract: a deferred release
// returns the charge even when the handler panics mid-body.
func TestInflightReleaseOnPanic(t *testing.T) {
	budget := NewInflight("test_panic", 1000, 1000)
	handler := func(_ http.ResponseWriter, r *http.Request) {
		defer budget.Admit(r)()
		_, err := io.ReadFull(r.Body, make([]byte, 300))
		require.NoError(t, err)
		require.Equal(t, int64(300), budget.InUse())
		panic("handler failure")
	}
	require.Panics(t, func() { handler(httptest.NewRecorder(), declaredRequest([]byte(strings.Repeat("p", 700)))) })
	require.Zero(t, budget.InUse(), "a panicking handler must release its charge")
}

// TestInflightConcurrentChargesStayWithinCapacity races many bodies; the
// charged total never exceeds the capacity and every charge is released.
func TestInflightConcurrentChargesStayWithinCapacity(t *testing.T) {
	const capacity = 640 << 10
	budget := NewInflight("test_race", capacity, 256<<10)
	var (
		wg       sync.WaitGroup
		peak     atomic.Int64
		admitted atomic.Int64
		odd      = make(chan error, 64)
	)
	observe := func() {
		for {
			current, highest := budget.InUse(), peak.Load()
			if current <= highest || peak.CompareAndSwap(highest, current) {
				return
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
			release := budget.Admit(request)
			defer release()
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
		require.NoError(t, err, "only budget refusals may fail a read")
	}
	require.LessOrEqual(t, peak.Load(), int64(capacity), "the budget must never be overcharged")
	require.Positive(t, admitted.Load())
	require.Zero(t, budget.InUse(), "every charge is released")
}

// TestInflightIgnoresBodylessRequests keeps streams and GETs uncharged.
func TestInflightIgnoresBodylessRequests(t *testing.T) {
	budget := NewInflight("test_bodyless", 10, 10)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	budget.Admit(request)()
	require.Zero(t, budget.InUse())
	var nilBudget *Inflight
	request = declaredRequest([]byte("x"))
	nilBudget.Admit(request)()
	data, err := io.ReadAll(request.Body)
	require.NoError(t, err, "a nil budget admits everything")
	require.Equal(t, "x", string(data))
}
