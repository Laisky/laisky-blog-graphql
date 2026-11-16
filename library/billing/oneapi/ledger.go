package oneapi

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// RateLimitWindow is the window of one-api's per-IP REST rate limit
// (GLOBAL_API_RATE_LIMIT, by default 480 calls per 3 minutes). one-api counts
// the calls it accepted from one client address within the trailing window,
// over every route of its /api group.
const RateLimitWindow = 3 * time.Minute

// Routes of the /api calls this server makes, as counted in
// oneapi_rest_calls_total{route}. The set is fixed: a label never comes
// from request data.
const (
	// RouteTokenBalance is GET /api/token/balance: API key validation,
	// revalidation and the balance proxies.
	RouteTokenBalance = "token_balance"
	// RouteTokenConsume is POST /api/token/consume: every tool call's charge
	// or zero-cost usage report.
	RouteTokenConsume = "token_consume"
)

// CallLedger counts the calls this process started to one-api's REST API
// (the /api group, which one-api limits per client address) over the
// trailing RateLimitWindow, so admission control can see the combined
// traffic of every caller sharing the limit. It counts calls when they start,
// which overcounts calls one-api refused: one-api records only accepted
// calls, so the ledger errs on the side of starting fewer.
//
// Counts are per process. Replicas behind one egress address add up at
// one-api; their configured limits must divide one-api's.
type CallLedger struct {
	mu sync.Mutex
	// seconds is the Unix second each slot counts; slots of older seconds
	// are stale and count zero.
	seconds []int64
	counts  []int
}

// NewCallLedger returns a ledger over window, at one-second resolution. One
// slot more than the window keeps the count conservative against one-api's
// whole-second comparison of call timestamps.
func NewCallLedger(window time.Duration) *CallLedger {
	slots := max(int(window/time.Second), 1) + 1
	return &CallLedger{seconds: make([]int64, slots), counts: make([]int, slots)}
}

// defaultLedger is the process-wide ledger of every one-api REST call.
var defaultLedger = NewCallLedger(RateLimitWindow)

// DefaultCallLedger returns the process-wide ledger that ConsumeAt records
// into and that the API key gate (internal/mcp/auth) admits against.
func DefaultCallLedger() *CallLedger {
	return defaultLedger
}

// restCalls counts every one-api REST call this process started, by route.
var restCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "oneapi_rest_calls_total",
	Help: "Calls this process started to one-api's per-IP-limited REST API (/api), by route.",
}, []string{"route"})

// restCallsInWindow reads the process-wide ledger: the calls started within
// one-api's rate-limit window. Compare it with the configured
// settings.mcp.auth.validation.oneapi_rate_limit (mcp_auth_oneapi_rate_limit).
var restCallsInWindow = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
	Name: "oneapi_rest_calls_in_window",
	Help: "one-api REST calls this process started within one-api's 3-minute per-IP rate-limit window.",
}, func() float64 { return float64(defaultLedger.Count(time.Now())) })

func init() {
	prometheus.MustRegister(restCalls, restCallsInWindow)
}

// Record counts one call to route that starts at now, unconditionally. Calls
// that must not be refused locally (a tool call's consume) use it.
func (l *CallLedger) Record(now time.Time, route string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.add(now.Unix())
	l.mu.Unlock()
	restCalls.WithLabelValues(route).Inc()
}

// TryRecord counts one call to route that starts at now only while fewer than
// limit calls were started within the window, and reports whether it did. A
// limit of 0 or less means the egress is exempt: the call is always counted.
func (l *CallLedger) TryRecord(now time.Time, route string, limit int) bool {
	if l == nil {
		return true
	}
	second := now.Unix()
	l.mu.Lock()
	if limit > 0 && l.count(second) >= limit {
		l.mu.Unlock()
		return false
	}
	l.add(second)
	l.mu.Unlock()
	restCalls.WithLabelValues(route).Inc()
	return true
}

// Count returns how many calls were started within the window ending at now.
func (l *CallLedger) Count(now time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count(now.Unix())
}

// Available reports whether one more call could start at now under limit
// (always, for a limit of 0 or less), without counting it.
func (l *CallLedger) Available(now time.Time, limit int) bool {
	return limit <= 0 || l.Count(now) < limit
}

// add counts one call in the slot of second. The caller holds l.mu.
func (l *CallLedger) add(second int64) {
	slots := int64(len(l.seconds))
	slot := int(((second % slots) + slots) % slots) // non-negative for any Unix second
	if l.seconds[slot] != second {
		l.seconds[slot], l.counts[slot] = second, 0
	}
	l.counts[slot]++
}

// count sums the slots still inside the window ending at second. The caller
// holds l.mu.
func (l *CallLedger) count(second int64) int {
	oldest := second - int64(len(l.seconds)) + 1
	total := 0
	for slot, at := range l.seconds {
		if at >= oldest && at <= second {
			total += l.counts[slot]
		}
	}
	return total
}
