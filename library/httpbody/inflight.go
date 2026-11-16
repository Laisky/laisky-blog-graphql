package httpbody

import (
	"context"
	"io"
	"net/http"
	"sync"

	errors "github.com/Laisky/errors/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// Refusal reasons of http_request_body_inflight_refused_total.
const (
	// InflightRefusedCapacity is a read refused because the budget is full.
	InflightRefusedCapacity = "capacity"
	// InflightRefusedTenantShare is a read refused because the request's
	// tenant already holds its share of the budget.
	InflightRefusedTenantShare = "tenant_share"
)

// Collectors of the in-flight body budgets. Entries and reasons come from
// small fixed sets (a transport class, the reasons above), never from
// request data.
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
		Help: "Request-body reads refused by an in-flight body budget, by entry and reason (capacity, tenant_share).",
	}, []string{entryLabel, "reason"})
	unclassifiedRefused = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "http_request_body_unclassified_refused_total",
		Help: "Request-body reads refused because a request read more than its pending bound before it was assigned to a budget.",
	})
)

func init() {
	prometheus.MustRegister(inflightBytes, inflightCapacity, inflightRefused, unclassifiedRefused)
}

// InflightExhaustedError reports that a request's body may not be read
// further: its budget (or its tenant's share of it) is spent, or it read more
// than its pending bound before it was assigned to a budget. Callers answer
// it with a retryable 503.
type InflightExhaustedError struct {
	// Capacity is the bound that was hit: the budget's capacity, the tenant
	// share, or the pending bound.
	Capacity int64
}

// Error describes the spent budget.
func (e *InflightExhaustedError) Error() string {
	return "in-flight request body budget exhausted"
}

// IsInflightExhausted reports whether err means an in-flight body budget
// refused to hold more of the request's body.
func IsInflightExhausted(err error) bool {
	var exhausted *InflightExhaustedError
	return errors.As(err, &exhausted)
}

// Inflight bounds the request-body bytes held by the in-flight requests of
// one class of callers (for example verified or anonymous MCP requests). It
// never blocks: a read that cannot be charged fails, so a full budget cannot
// queue connections.
//
// A body is charged exactly the bytes it has delivered, as they are read
// (see Charge): a declared Content-Length that has not arrived costs nothing,
// so slow or stalled senders cannot reserve capacity they do not use.
// Handlers must therefore let their buffers grow with the received bytes
// (io.ReadAll, bytes.Buffer.ReadFrom) instead of allocating a declared length
// up front, or the charge would understate the memory.
//
// A budget with a tenant share also bounds what the requests of one tenant
// (a verified API key) hold together, so one caller cannot starve the
// others. Requests without a tenant are bounded by the capacity only.
//
// The budget counts body bytes, not heap: every copy a handler chain keeps of
// a body (a classification buffer, an SDK's own io.ReadAll) adds to the real
// memory, so operators size it as a fraction of the memory reserved for
// request bodies.
type Inflight struct {
	entry    string
	capacity int64
	share    int64
	gauge    prometheus.Gauge

	mu      sync.Mutex
	used    int64
	tenants map[string]int64
}

// NewInflight returns a budget of capacity bytes labeled entry in the
// metrics. maxBodyBytes is the largest body one request may carry; capacity
// is raised to it, so a single body within the per-request cap always fits an
// idle budget. tenantShare bounds the bytes one tenant may hold; it is raised
// to maxBodyBytes and kept within the capacity, and zero or negative disables
// the share.
func NewInflight(entry string, capacity, maxBodyBytes, tenantShare int64) *Inflight {
	capacity = max(capacity, maxBodyBytes, 1)
	if tenantShare > 0 {
		tenantShare = min(max(tenantShare, maxBodyBytes), capacity)
	} else {
		tenantShare = 0
	}
	inflightCapacity.WithLabelValues(entry).Set(float64(capacity))
	return &Inflight{
		entry:    entry,
		capacity: capacity,
		share:    tenantShare,
		gauge:    inflightBytes.WithLabelValues(entry),
		tenants:  make(map[string]int64),
	}
}

// Capacity returns the budget's total capacity in bytes.
func (f *Inflight) Capacity() int64 {
	return f.capacity
}

// TenantShare returns the most one tenant may hold, or zero without a share.
func (f *Inflight) TenantShare() int64 {
	return f.share
}

// InUse returns the bytes currently charged.
func (f *Inflight) InUse() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used
}

// TenantInUse returns the bytes currently charged to tenant.
func (f *Inflight) TenantInUse(tenant string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tenants[tenant]
}

// tryAcquire charges n bytes to tenant when they fit, without waiting. It
// returns the refusal's error, counted in the metrics, when they do not.
func (f *Inflight) tryAcquire(tenant string, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case n > f.capacity-f.used:
		inflightRefused.WithLabelValues(f.entry, InflightRefusedCapacity).Inc()
		return errors.WithStack(&InflightExhaustedError{Capacity: f.capacity})
	case f.share > 0 && tenant != "" && n > f.share-f.tenants[tenant]:
		inflightRefused.WithLabelValues(f.entry, InflightRefusedTenantShare).Inc()
		return errors.WithStack(&InflightExhaustedError{Capacity: f.share})
	}
	f.used += n
	if f.share > 0 && tenant != "" && n > 0 {
		f.tenants[tenant] += n
	}
	f.gauge.Add(float64(n))
	return nil
}

// release returns n bytes charged to tenant.
func (f *Inflight) release(tenant string, n int64) {
	if n <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used -= n
	if held, ok := f.tenants[tenant]; ok {
		if held <= n {
			delete(f.tenants, tenant)
		} else {
			f.tenants[tenant] = held - n
		}
	}
	f.gauge.Sub(float64(n))
}

// Charge is the in-flight accounting of one request body (Track). Bytes read
// before the request is classified are pending: they are not charged to any
// budget, and at most maxPending of them may be read. Assign then charges the
// pending bytes to the budget of the request's class and tenant, and every
// later read is charged there too. Release returns everything. A nil Charge
// accounts nothing and refuses nothing.
type Charge struct {
	mu         sync.Mutex
	maxPending int64
	pending    int64
	budget     *Inflight
	tenant     string
	charged    int64
	released   bool
	err        error
}

// chargeKey stores a request's Charge in its context.
type chargeKey struct{}

// Track wraps r's body so every byte it delivers is accounted by the
// returned Charge, which is also stored in the returned request's context
// (ChargeFromContext), because later middleware may replace r.Body. Until
// Assign, at most maxPending bytes may be read; further reads fail with an
// *InflightExhaustedError. A request without a body gets a Charge too, so
// Assign and Release need no special case. Defer Release.
func Track(r *http.Request, maxPending int64) (*http.Request, *Charge) {
	charge := &Charge{maxPending: max(maxPending, 0)}
	if r == nil {
		return r, charge
	}
	tracked := r.WithContext(context.WithValue(r.Context(), chargeKey{}, charge))
	if r.Body != nil && r.Body != http.NoBody {
		tracked.Body = &trackedBody{ReadCloser: r.Body, charge: charge}
	}
	return tracked, charge
}

// ChargeFromContext returns the Charge Track stored on ctx.
func ChargeFromContext(ctx context.Context) (*Charge, bool) {
	if ctx == nil {
		return nil, false
	}
	charge, ok := ctx.Value(chargeKey{}).(*Charge)
	return charge, ok && charge != nil
}

// Assign classifies the request: it charges the bytes read so far to budget
// on behalf of tenant ("" for none) and makes every later read charge it as
// well. It returns an *InflightExhaustedError, and fails every later read,
// when those bytes do not fit the budget or the tenant's share; the caller
// then answers 503. A request may be assigned once; a nil Charge or budget
// assigns nothing.
func (c *Charge) Assign(budget *Inflight, tenant string) error {
	if c == nil || budget == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.err != nil:
		return c.err
	case c.released:
		return nil
	case c.budget != nil:
		return errors.New("request body already assigned to an in-flight budget")
	}
	if err := budget.tryAcquire(tenant, c.pending); err != nil {
		c.err = err
		return c.err
	}
	c.budget, c.tenant, c.charged, c.pending = budget, tenant, c.pending, 0
	return nil
}

// Pending returns the bytes read and not yet assigned to a budget.
func (c *Charge) Pending() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

// Charged returns the bytes charged to the assigned budget.
func (c *Charge) Charged() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.charged
}

// Release returns the request's charge to its budget, once; later reads are
// no longer accounted. Extra calls are no-ops.
func (c *Charge) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return
	}
	c.released = true
	if c.budget != nil {
		c.budget.release(c.tenant, c.charged)
	}
	c.charged, c.pending = 0, 0
}

// account records n delivered bytes: pending before Assign, charged after.
func (c *Charge) account(n int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.err != nil:
		return c.err
	case c.released:
		return nil
	case c.budget == nil:
		if n > c.maxPending-c.pending {
			unclassifiedRefused.Inc()
			c.err = errors.WithStack(&InflightExhaustedError{Capacity: c.maxPending})
			return c.err
		}
		c.pending += n
		return nil
	}
	if err := c.budget.tryAcquire(c.tenant, n); err != nil {
		c.err = err
		return c.err
	}
	c.charged += n
	return nil
}

// failed returns the error that ended the body's reads, if any.
func (c *Charge) failed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// trackedBody accounts every delivered byte through its Charge.
type trackedBody struct {
	io.ReadCloser
	charge *Charge
}

// Read forwards to the body and accounts the bytes it delivered. A refused
// read fails this and every later read; the bytes of the refused read are
// dropped, because the request is being refused anyway.
func (b *trackedBody) Read(p []byte) (int, error) {
	if err := b.charge.failed(); err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if accountErr := b.charge.account(int64(n)); accountErr != nil {
			return 0, accountErr
		}
	}
	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must be returned unwrapped
}
