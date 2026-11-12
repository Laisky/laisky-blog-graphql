package auth

import (
	"sync"
	"time"
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
