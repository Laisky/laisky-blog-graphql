package httpbody

import (
	"io"
	"net/http"
	"sync"

	errors "github.com/Laisky/errors/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// Collectors of the in-flight body budgets, by entry. Entries come from a
// small fixed set (a transport or route class), never from request data.
var (
	inflightBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http_request_body_inflight_bytes",
		Help: "Request-body bytes read and not yet released by in-flight requests, by entry.",
	}, []string{entryLabel})
	inflightCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http_request_body_inflight_capacity_bytes",
		Help: "Capacity of the in-flight body budget, by entry.",
	}, []string{entryLabel})
	inflightRefused = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_body_inflight_refused_total",
		Help: "Request-body reads refused because the in-flight body budget was spent, by entry.",
	}, []string{entryLabel})
)

func init() {
	prometheus.MustRegister(inflightBytes, inflightCapacity, inflightRefused)
}

// InflightExhaustedError reports that the in-flight body budget could not
// hold more of a request's body. Callers answer it with a retryable 503.
type InflightExhaustedError struct {
	// Capacity is the budget's total capacity in bytes.
	Capacity int64
}

// Error describes the spent budget.
func (e *InflightExhaustedError) Error() string {
	return "in-flight request body budget exhausted"
}

// IsInflightExhausted reports whether err means the in-flight body budget
// refused to hold more of the request's body.
func IsInflightExhausted(err error) bool {
	var exhausted *InflightExhaustedError
	return errors.As(err, &exhausted)
}

// Inflight bounds the request-body bytes held by the in-flight requests of
// one entry (for example the MCP transport). It never blocks: a read that
// cannot be charged fails, so a full budget cannot queue connections.
//
// A body is charged exactly the bytes it has delivered, as they are read,
// whether or not it declared a Content-Length: a declared length that has not
// arrived costs nothing, so slow or stalled senders cannot reserve capacity
// they do not use. Handlers must therefore let their buffers grow with the
// received bytes (io.ReadAll, bytes.Buffer.ReadFrom) instead of allocating a
// declared length up front, or the charge would understate the memory. The
// byte cap of the route (Enforce, Tighten) bounds what one body can be
// charged. The charge is released once, when the request's handler returns.
//
// The budget counts body bytes, not heap: every copy a handler chain keeps of
// a body (a classification buffer, an SDK's own io.ReadAll) adds to the real
// memory, so operators size it as a fraction of the memory reserved for
// request bodies.
type Inflight struct {
	entry    string
	capacity int64
	gauge    prometheus.Gauge
	refused  prometheus.Counter

	mu   sync.Mutex
	used int64
}

// NewInflight returns a budget of capacity bytes for entry. maxBodyBytes is
// the largest body one request may carry; capacity is raised to it when
// smaller, so a single body within the per-request cap always fits an idle
// budget. The capacity is at least one byte.
func NewInflight(entry string, capacity, maxBodyBytes int64) *Inflight {
	capacity = max(capacity, maxBodyBytes, 1)
	inflightCapacity.WithLabelValues(entry).Set(float64(capacity))
	return &Inflight{
		entry:    entry,
		capacity: capacity,
		gauge:    inflightBytes.WithLabelValues(entry),
		refused:  inflightRefused.WithLabelValues(entry),
	}
}

// Capacity returns the budget's total capacity in bytes.
func (f *Inflight) Capacity() int64 {
	return f.capacity
}

// InUse returns the bytes currently charged.
func (f *Inflight) InUse() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used
}

// tryAcquire charges n bytes when they fit, without waiting.
func (f *Inflight) tryAcquire(n int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > f.capacity-f.used {
		return false
	}
	f.used += n
	f.gauge.Add(float64(n))
	return true
}

// release returns n charged bytes.
func (f *Inflight) release(n int64) {
	if n <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used -= n
	f.gauge.Sub(float64(n))
}

// Admit makes r's body charge the budget for every byte it delivers and
// returns the function that releases the charge; call it once when the
// handler returns (defer it, so a panic releases too; extra calls are
// no-ops). Nothing is charged up front. A read whose bytes do not fit fails
// with an *InflightExhaustedError, as does every later read of that body.
// A request without a body is not wrapped.
func (f *Inflight) Admit(r *http.Request) func() {
	if f == nil || r == nil || r.Body == nil || r.Body == http.NoBody {
		return func() {}
	}
	body := &inflightBody{ReadCloser: r.Body, budget: f}
	r.Body = body
	return body.release
}

// inflightBody charges a body's delivered bytes to the budget.
type inflightBody struct {
	io.ReadCloser
	budget *Inflight

	mu       sync.Mutex
	charged  int64
	released bool
	err      error
}

// Read forwards to the body and charges the bytes it delivered. A refused
// charge fails this and every later read; the bytes of the refused read are
// dropped, because the request is being refused anyway.
func (b *inflightBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	failed := b.err
	b.mu.Unlock()
	if failed != nil {
		return 0, failed
	}
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if chargeErr := b.charge(int64(n)); chargeErr != nil {
			return 0, chargeErr
		}
	}
	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must be returned unwrapped
}

// charge accounts n delivered bytes. After release the body is no longer
// charged (a late read cannot leak a charge).
func (b *inflightBody) charge(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return nil
	}
	if !b.budget.tryAcquire(n) {
		b.budget.refused.Inc()
		b.err = errors.WithStack(&InflightExhaustedError{Capacity: b.budget.capacity})
		return b.err
	}
	b.charged += n
	return nil
}

// release returns the body's whole charge to the budget, once.
func (b *inflightBody) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return
	}
	b.released = true
	b.budget.release(b.charged)
	b.charged = 0
}
