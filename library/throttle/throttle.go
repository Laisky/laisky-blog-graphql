// Package throttle rate limiters
package throttle

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
)

// tokenEpsilon is the tolerance, in sends, for a budget that has refilled to
// exactly a whole send or to its burst. Durations are whole nanoseconds, so a
// refill can land just short of the exact value: a third of a second at 3 sends/s
// is 333333333ns, which earns 0.999999999 sends. At any rate up to 1000 sends/s
// that shortfall is at most 1e-6 send, while float64 rounding adds about 1e-16.
// The tolerance stays far below one send, so it never lets an extra send through.
const tokenEpsilon = 1e-6

var (
	// ErrTotalBudgetExhausted reports that the budget shared by every alert type has no send left.
	ErrTotalBudgetExhausted = errors.New("telegram alert total budget exhausted")
	// ErrTitleBudgetExhausted reports that the alert type's own budget has no send left.
	ErrTitleBudgetExhausted = errors.New("telegram alert type budget exhausted")
	// ErrAdmissionSettled reports an Admission that was already authorized or canceled.
	ErrAdmissionSettled = errors.New("telegram alert admission already settled")
)

// TelegramThrottleCfg configuration for TelegramThrottle.
type TelegramThrottleCfg struct {
	// TotalNPerSec and TotalBurst configure the budget shared by every alert type.
	TotalNPerSec, TotalBurst int
	// EachTitleNPerSec and EachTitleBurst configure each alert type's own budget.
	EachTitleNPerSec, EachTitleBurst int

	// TotleNPerSec is the historical spelling of TotalNPerSec, honored when
	// TotalNPerSec is zero.
	//
	// Deprecated: use TotalNPerSec.
	TotleNPerSec int
	// TotleBurst is the historical spelling of TotalBurst, honored when
	// TotalBurst is zero.
	//
	// Deprecated: use TotalBurst.
	TotleBurst int
}

// Validate reports why cfg cannot build a TelegramThrottle. It has no side
// effects, so callers may use it to check configuration without building one.
func (cfg *TelegramThrottleCfg) Validate() error {
	_, _, err := cfg.totals()
	return err
}

// totals returns the shared budget's rate and burst after validating cfg. A
// correctly spelled field wins over its deprecated spelling; both set to
// different values is an error rather than a silent choice.
func (cfg *TelegramThrottleCfg) totals() (rate, burst int, err error) {
	if cfg == nil {
		return 0, 0, errors.New("telegram throttle config is nil")
	}
	if rate, err = preferredSetting("TotalNPerSec", cfg.TotalNPerSec, cfg.TotleNPerSec); err != nil {
		return 0, 0, err
	}
	if burst, err = preferredSetting("TotalBurst", cfg.TotalBurst, cfg.TotleBurst); err != nil {
		return 0, 0, err
	}
	if rate <= 0 || cfg.EachTitleNPerSec <= 0 {
		return 0, 0, errors.Errorf("NPerSec must bigger than 0")
	}
	if burst < rate || cfg.EachTitleBurst < cfg.EachTitleNPerSec {
		return 0, 0, errors.Errorf("burst must bigger than NPerSec")
	}
	return rate, burst, nil
}

// preferredSetting returns current, or deprecated when current is zero.
func preferredSetting(field string, current, deprecated int) (int, error) {
	if current != 0 && deprecated != 0 && current != deprecated {
		return 0, errors.Errorf("%s is %d but its deprecated spelling is %d", field, current, deprecated)
	}
	if current != 0 {
		return current, nil
	}
	return deprecated, nil
}

// TelegramThrottle throttle for telegram monitor alerts.
//
// It keeps one total budget shared by every alert type, which bounds credential
// lookups and sends, and one budget per alert type, which keeps one noisy type
// from starving the others. Both are token buckets that refill from elapsed time
// when read, so the throttle runs no goroutine and needs no shutdown.
//
// A caller that has not authenticated the alert yet uses Admit before the
// credential lookup, then settles the returned Admission. Admit reserves a send
// from both budgets in one step, so concurrent requests for one type cannot spend
// more of the total budget than the type has sends. A failed authentication
// returns the reserved per-type send through Cancel.
//
// A missing per-type budget is a full one. A budget is dropped as soon as it is
// full again, so retained state is bounded by recent sends rather than by the
// number of alert types, and dropping it changes no decision.
type TelegramThrottle struct {
	totalRate, totalBurst float64
	titleRate, titleBurst float64
	// sweepEvery is how long an empty per-type budget takes to refill fully.
	sweepEvery time.Duration
	now        func() time.Time

	mu        sync.Mutex
	total     bucket
	titles    map[string]*bucket
	lastSweep time.Time
	stats     TelegramThrottleStats
}

// TelegramThrottleStats is a snapshot of a TelegramThrottle's counters.
type TelegramThrottleStats struct {
	// Admitted counts sends reserved by Admit, each of which spent a total send.
	Admitted uint64
	// Authorized counts admissions settled by Authorize.
	Authorized uint64
	// Canceled counts admissions whose per-type send was returned by Cancel.
	Canceled uint64
	// TotalRefusals counts refusals because the shared budget was empty.
	TotalRefusals uint64
	// TitleRefusals counts refusals because an alert type's own budget was empty.
	TitleRefusals uint64
	// RetainedTitles is the number of per-type budgets currently held.
	RetainedTitles int
}

// Admission is one alert's reserved send, held while its credentials are checked.
// Settle it exactly once: Authorize after the credentials matched, Cancel
// otherwise. Cancel is a no-op after Authorize, so callers may defer it right
// after Admit.
type Admission struct {
	throttle *TelegramThrottle
	name     string
	budget   *bucket
	// settled is guarded by throttle.mu.
	settled bool
}

// bucket is a token bucket refilled from elapsed time when it is read.
type bucket struct {
	tokens float64
	last   time.Time
}

// NewTelegramThrottle builds a TelegramThrottle from cfg. Every budget starts
// full. The throttle starts no goroutine, so ctx bounds nothing and is kept only
// for API compatibility.
func NewTelegramThrottle(_ context.Context, cfg *TelegramThrottleCfg) (*TelegramThrottle, error) {
	totalRate, totalBurst, err := cfg.totals()
	if err != nil {
		return nil, errors.Wrap(err, "validate telegram throttle config")
	}

	t := &TelegramThrottle{
		totalRate:  float64(totalRate),
		totalBurst: float64(totalBurst),
		titleRate:  float64(cfg.EachTitleNPerSec),
		titleBurst: float64(cfg.EachTitleBurst),
		sweepEvery: time.Duration(float64(time.Second) * float64(cfg.EachTitleBurst) / float64(cfg.EachTitleNPerSec)),
		now:        time.Now,
		titles:     make(map[string]*bucket),
	}
	now := t.now()
	t.total = bucket{tokens: t.totalBurst, last: now}
	t.lastSweep = now
	return t, nil
}

// Admit reserves one send for alertName before its credentials are checked. It
// refuses with ErrTitleBudgetExhausted when alertName's own budget has no send
// left, and with ErrTotalBudgetExhausted when the shared budget is empty; a
// refusal spends nothing.
//
// alertName is untrusted and must already be in its canonical form, the form the
// credential lookup matches. A budget created for it is dropped again when
// Cancel returns its send, so names that never authenticate keep no state.
func (t *TelegramThrottle) Admit(alertName string) (*Admission, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.sweepLocked(now)

	budget := t.titles[alertName]
	if budget != nil {
		budget.refill(now, t.titleRate, t.titleBurst)
		if !budget.hasSend() {
			t.stats.TitleRefusals++
			return nil, errors.WithStack(ErrTitleBudgetExhausted)
		}
	}
	t.total.refill(now, t.totalRate, t.totalBurst)
	if !t.total.hasSend() {
		t.stats.TotalRefusals++
		return nil, errors.WithStack(ErrTotalBudgetExhausted)
	}
	if budget == nil {
		budget = &bucket{tokens: t.titleBurst, last: now}
		t.titles[alertName] = budget
	}
	t.total.take()
	budget.take()
	t.stats.Admitted++
	return &Admission{throttle: t, name: alertName, budget: budget}, nil
}

// Allow spends one send from the shared budget and one from alertName's own
// budget in one step, and spends neither when either is empty. It is for a caller
// that already verified alertName's credentials, such as a webhook that
// authenticates before it admits. A caller that has not authenticated yet must
// use Admit, so that requests with wrong credentials cannot drain a known type.
func (t *TelegramThrottle) Allow(alertName string) bool {
	admission, err := t.Admit(alertName)
	if err != nil {
		return false
	}
	return admission.Authorize(alertName) == nil
}

// Stats returns a snapshot of the throttle's counters.
func (t *TelegramThrottle) Stats() TelegramThrottleStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := t.stats
	stats.RetainedTitles = len(t.titles)
	return stats
}

// Authorize settles a once the credentials of storedName, the stored name of the
// alert, matched, and keeps the reserved send spent. storedName is normally the
// admitted name. If storage matched a differently spelled name, the reservation
// moves to storedName's budget, which may refuse it with ErrTitleBudgetExhausted.
func (a *Admission) Authorize(storedName string) error {
	t := a.throttle
	t.mu.Lock()
	defer t.mu.Unlock()

	if a.settled {
		return errors.WithStack(ErrAdmissionSettled)
	}
	a.settled = true
	if storedName == a.name {
		t.stats.Authorized++
		return nil
	}

	now := t.now()
	t.refundLocked(a, now)
	budget := t.titles[storedName]
	if budget == nil {
		budget = &bucket{tokens: t.titleBurst, last: now}
		t.titles[storedName] = budget
	}
	budget.refill(now, t.titleRate, t.titleBurst)
	if !budget.hasSend() {
		t.stats.TitleRefusals++
		return errors.WithStack(ErrTitleBudgetExhausted)
	}
	budget.take()
	t.stats.Authorized++
	return nil
}

// Cancel returns a's reserved send to its alert type's budget, for an alert whose
// credentials did not match or could not be checked. The shared send stays spent,
// because it paid for the credential lookup. Cancel is a no-op on a nil or settled
// admission.
func (a *Admission) Cancel() {
	if a == nil {
		return
	}
	t := a.throttle
	t.mu.Lock()
	defer t.mu.Unlock()

	if a.settled {
		return
	}
	a.settled = true
	t.stats.Canceled++
	t.refundLocked(a, t.now())
}

// refundLocked returns a's reserved send and drops the budget if that leaves it
// full. A budget dropped since Admit was full, which already includes the send.
// The caller holds t.mu.
func (t *TelegramThrottle) refundLocked(a *Admission, now time.Time) {
	if t.titles[a.name] != a.budget {
		return
	}
	a.budget.refill(now, t.titleRate, t.titleBurst)
	a.budget.tokens = min(t.titleBurst, a.budget.tokens+1)
	if a.budget.full(t.titleBurst) {
		delete(t.titles, a.name)
	}
}

// sweepLocked drops every per-type budget that has refilled fully. It scans at
// most once per sweepEvery, the time an empty budget takes to refill, so each
// budget outlives its last send by at most twice that. The caller holds t.mu.
func (t *TelegramThrottle) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < t.sweepEvery {
		return
	}
	t.lastSweep = now
	for name, budget := range t.titles {
		budget.refill(now, t.titleRate, t.titleBurst)
		if budget.full(t.titleBurst) {
			delete(t.titles, name)
		}
	}
}

// refill credits b with the sends earned since it was last read, up to burst.
// A clock that steps backwards earns nothing.
func (b *bucket) refill(now time.Time, rate, burst float64) {
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.tokens = min(burst, b.tokens+elapsed.Seconds()*rate)
}

// hasSend reports whether b holds at least one send.
func (b *bucket) hasSend() bool {
	return b.tokens+tokenEpsilon >= 1
}

// full reports whether b holds its whole burst.
func (b *bucket) full(burst float64) bool {
	return b.tokens+tokenEpsilon >= burst
}

// take spends one send from b, which must hold one.
func (b *bucket) take() {
	b.tokens = max(0, b.tokens-1)
}
