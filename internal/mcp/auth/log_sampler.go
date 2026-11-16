package auth

import (
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

// LogSampler admits at most one log line per interval for a noisy event and
// counts the occurrences it suppressed, so a flood produces one summary per
// interval instead of one WARN line per request.
type LogSampler struct {
	mu         sync.Mutex
	interval   time.Duration
	next       time.Time
	suppressed int64
}

// NewLogSampler returns a sampler emitting at most once per interval.
func NewLogSampler(interval time.Duration) *LogSampler {
	return &LogSampler{interval: interval}
}

// Allow reports whether the event at now should be logged, and how many
// events were suppressed since the previous admitted one.
func (s *LogSampler) Allow(now time.Time) (bool, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.next) {
		s.suppressed++
		return false, 0
	}
	suppressed := s.suppressed
	s.suppressed = 0
	s.next = now.Add(s.interval)
	return true, suppressed
}

// Causes of an unavailable verdict, as logged in the "unavailable_cause" field.
const (
	// UnavailableCauseLocalBudget means this server's own upstream budget
	// refused the call: a capacity limit, not an outage (ErrUpstreamThrottled).
	UnavailableCauseLocalBudget = "local_budget"
	// UnavailableCauseAuthority means one-api gave no verdict: a timeout,
	// transport error, 429, 5xx, redirect or a non-success 200, or such a
	// failure moments ago for the same key.
	UnavailableCauseAuthority = "authority"
	// UnavailableCauseLocal means neither: the request ended while waiting
	// for a shared verdict, or no authenticator is installed.
	UnavailableCauseLocal = "local"
)

// errAuthorityNoVerdict marks unavailable verdicts that one-api caused.
var errAuthorityNoVerdict = errors.New("the api key authority gave no verdict")

// UnavailableCause classifies an error for which IsUnavailable is true.
func UnavailableCause(err error) string {
	switch {
	case errors.Is(err, ErrUpstreamThrottled):
		return UnavailableCauseLocalBudget
	case errors.Is(err, errAuthorityNoVerdict):
		return UnavailableCauseAuthority
	default:
		return UnavailableCauseLocal
	}
}

// UnavailableLog samples "validation unavailable" WARN summaries separately
// per cause, so a spent local budget (capacity) stays visible during a
// one-api outage and the other way round; other occurrences log at DEBUG.
type UnavailableLog struct {
	samplers map[string]*LogSampler
}

// NewUnavailableLog returns an UnavailableLog emitting at most one WARN per
// cause and interval.
func NewUnavailableLog(interval time.Duration) *UnavailableLog {
	return &UnavailableLog{samplers: map[string]*LogSampler{
		UnavailableCauseLocalBudget: NewLogSampler(interval),
		UnavailableCauseAuthority:   NewLogSampler(interval),
		UnavailableCauseLocal:       NewLogSampler(interval),
	}}
}

// Log records one unavailable verdict for err with message and fields.
func (l *UnavailableLog) Log(logger glog.Logger, message string, err error, fields ...zap.Field) {
	if l == nil || logger == nil {
		return
	}
	cause := UnavailableCause(err)
	sampler := l.samplers[cause]
	fields = append(fields, zap.String("unavailable_cause", cause))
	if emit, suppressed := sampler.Allow(time.Now()); emit {
		logger.Warn(message, append(fields, zap.Int64("suppressed_since_last", suppressed), zap.Error(err))...)
		return
	}
	logger.Debug(message, append(fields, zap.Error(err))...)
}
