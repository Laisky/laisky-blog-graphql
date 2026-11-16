package auth

import (
	"sync"
	"time"
)

// outageWindow is how long one authority failure keeps the outage window
// open: two retry intervals, so one failed probe per interval keeps it open
// and a single answered probe, or two intervals without any real failure,
// closes it.
const outageWindow = 2 * unavailableRetry

// authorityOutage tracks whether one-api itself recently failed to give a
// verdict, process-wide. While the window is open, a known key within its
// stale grace is served without an upstream call, and one caller per
// unavailableRetry probes one-api; without it, every known key would retry
// on its own, spend the budgets within seconds, and the resulting local
// refusals (which never grant stale grace) would turn an outage into 503s
// for most active users.
//
// Only a call that reached the authority and got no verdict opens the
// window, and only for failures that are not specific to the presented key
// (ErrKeySpecificNoVerdict), so neither a flood that empties this server's
// budgets nor a caller holding a broken token can open it. Every verdict
// (acceptance or rejection) closes it.
type authorityOutage struct {
	mu sync.Mutex
	// lastFailure is when one-api last failed for everyone; zero when closed.
	lastFailure time.Time
	// probeAfter is when the next caller may probe one-api.
	probeAfter time.Time
}

// failed records an authority failure observed at now.
func (o *authorityOutage) failed(now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastFailure = now
	o.probeAfter = now.Add(unavailableRetry)
}

// recovered closes the window: one-api gave a verdict.
func (o *authorityOutage) recovered() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastFailure = time.Time{}
}

// active reports whether one-api failed for everyone within outageWindow.
func (o *authorityOutage) active(now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.activeLocked(now)
}

// activeLocked is active for a caller holding o.mu.
func (o *authorityOutage) activeLocked(now time.Time) bool {
	return !o.lastFailure.IsZero() && now.Sub(o.lastFailure) < outageWindow
}

// claimProbe reports whether a known key's caller should ask one-api at now:
// always outside an outage, and inside one only for the first caller after
// each unavailableRetry. The others are served from stale grace.
func (o *authorityOutage) claimProbe(now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.activeLocked(now) {
		return true
	}
	if now.Before(o.probeAfter) {
		return false
	}
	o.probeAfter = now.Add(unavailableRetry)
	return true
}
