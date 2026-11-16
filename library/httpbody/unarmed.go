package httpbody

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// UnarmedWarnInterval bounds the WARN lines about unarmed read deadlines to
// one per entry and interval.
const UnarmedWarnInterval = time.Minute

// entryLabel is the metric label naming the transport or route class of a
// body budget; its values come from a small fixed set, never from requests.
const entryLabel = "entry"

// unarmedDeadlines counts requests whose read deadline could not be armed.
// Such a body is still capped in bytes, but nothing bounds how slowly it may
// arrive, so a rising count means the slow-body protection is off on that
// entry (for example a ResponseWriter wrapper without Unwrap).
var unarmedDeadlines = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "http_request_body_read_deadline_unarmed_total",
	Help: "Requests with a body whose read deadline could not be armed, by entry; only the byte cap bounds those bodies.",
}, []string{entryLabel})

func init() {
	prometheus.MustRegister(unarmedDeadlines)
}

// UnarmedReporter counts requests whose read deadline could not be armed and
// samples the WARN lines about them per entry. Each body-budget middleware
// owns one.
type UnarmedReporter struct {
	interval time.Duration

	mu         sync.Mutex
	next       map[string]time.Time
	suppressed map[string]int64
}

// NewUnarmedReporter returns a reporter admitting one WARN per entry and
// interval (UnarmedWarnInterval in production).
func NewUnarmedReporter(interval time.Duration) *UnarmedReporter {
	return &UnarmedReporter{
		interval:   interval,
		next:       make(map[string]time.Time),
		suppressed: make(map[string]int64),
	}
}

// Record notes that a request on entry got a body whose read deadline could
// not be armed (Result.DeadlineErr). It counts the request in
// http_request_body_read_deadline_unarmed_total and reports whether the
// caller should log a WARN now, at most once per interval and entry, and how
// many occurrences that WARN summarizes; callers log the others at DEBUG.
// entry must come from a small fixed set (a route class), never from request
// data, because it is a metric label.
func (s *UnarmedReporter) Record(entry string, now time.Time) (warn bool, suppressed int64) {
	unarmedDeadlines.WithLabelValues(entry).Inc()
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.next[entry]) {
		s.suppressed[entry]++
		return false, 0
	}
	suppressed = s.suppressed[entry]
	s.suppressed[entry] = 0
	s.next[entry] = now.Add(s.interval)
	return true, suppressed
}
