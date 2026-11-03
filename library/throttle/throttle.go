// Package throttle rate limiters
package throttle

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
)

// tokenEpsilon absorbs nanosecond truncation and float rounding when a refill
// lands exactly on a whole send.
const tokenEpsilon = 1e-6

// TelegramThrottleCfg configuration for TelegramThrottle
type TelegramThrottleCfg struct {
	TotleNPerSec, TotleBurst         int
	EachTitleNPerSec, EachTitleBurst int
}

// TelegramThrottle throttle for telegram monitor alerts.
//
// Admission happens in two steps around the alert's credential lookup, so that
// only authenticated alert names ever own state:
//
//   - AllowAttempt runs before the lookup. It spends one send from the shared
//     budget, which bounds lookups, unless the alert type is a known name whose
//     own budget is already empty.
//   - AllowAuthorized runs after the lookup succeeded. It spends one send from
//     the budget of the stored alert name, created on first use.
//
// Per-title budgets refill from elapsed time when read, so they need no goroutine.
type TelegramThrottle struct {
	cfg           *TelegramThrottleCfg
	totleThrottle *gutils.RateLimiter
	now           func() time.Time

	mu     sync.Mutex
	titles map[string]*titleBudget
}

// titleBudget is the token bucket of one authenticated alert name.
type titleBudget struct {
	tokens float64
	last   time.Time
}

// NewTelegramThrottle create new TelegramThrottle
func NewTelegramThrottle(ctx context.Context, cfg *TelegramThrottleCfg) (t *TelegramThrottle, err error) {
	if cfg.TotleNPerSec <= 0 || cfg.EachTitleNPerSec <= 0 {
		return nil, errors.Errorf("NPerSec must bigger than 0")
	}
	if cfg.TotleBurst < cfg.TotleNPerSec || cfg.EachTitleBurst < cfg.EachTitleNPerSec {
		return nil, errors.Errorf("burst must bigger than NPerSec")
	}

	var tt *gutils.RateLimiter
	if tt, err = gutils.NewRateLimiter(ctx, gutils.RateLimiterArgs{
		Max:     cfg.TotleBurst,
		NPerSec: cfg.TotleNPerSec,
	}); err != nil {
		return nil, errors.Wrap(err, "create totle throttle")
	}

	t = &TelegramThrottle{
		cfg:           cfg,
		totleThrottle: tt,
		now:           time.Now,
		titles:        make(map[string]*titleBudget),
	}
	return t, nil
}

// AllowAttempt reports whether an alert for alertType may proceed to its
// credential lookup. alertType is untrusted, so it is only looked up, never stored.
// A refusal by the shared budget leaves the alert type's own budget untouched.
func (t *TelegramThrottle) AllowAttempt(alertType string) bool {
	if t.titleExhausted(alertType) {
		return false
	}
	return t.totleThrottle.Allow()
}

// AllowAuthorized spends one send from the budget of alertName, which must be the
// stored name of an alert whose credentials were just verified.
func (t *TelegramThrottle) AllowAuthorized(alertName string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	budget, ok := t.titles[alertName]
	if !ok {
		// Like gutils.RateLimiter, a new budget starts with one second of sends.
		budget = &titleBudget{tokens: float64(t.cfg.EachTitleNPerSec), last: t.now()}
		t.titles[alertName] = budget
	} else {
		t.refill(budget)
	}
	if budget.tokens+tokenEpsilon < 1 {
		return false
	}
	budget.tokens--
	return true
}

// titleExhausted reports whether alertType already owns a budget with no send left.
func (t *TelegramThrottle) titleExhausted(alertType string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	budget, ok := t.titles[alertType]
	if !ok {
		return false
	}
	t.refill(budget)
	return budget.tokens+tokenEpsilon < 1
}

// refill credits budget with the sends earned since it was last read, up to the
// burst. The caller holds t.mu.
func (t *TelegramThrottle) refill(budget *titleBudget) {
	now := t.now()
	elapsed := now.Sub(budget.last)
	if elapsed <= 0 {
		return
	}
	budget.last = now
	budget.tokens = min(float64(t.cfg.EachTitleBurst), budget.tokens+elapsed.Seconds()*float64(t.cfg.EachTitleNPerSec))
}
