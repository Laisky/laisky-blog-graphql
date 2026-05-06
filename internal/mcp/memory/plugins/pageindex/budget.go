package pageindex

import (
	"sync/atomic"

	errors "github.com/Laisky/errors/v2"
)

// ErrBudgetExceeded is returned when the shared token budget runs out.
var ErrBudgetExceeded = errors.New("token budget exceeded")

// Budget is the §2.6.4.2 atomic gate shared across goroutines.
type Budget struct {
	remaining atomic.Int64
}

// NewBudget seeds the gate with the supplied initial token count.
func NewBudget(initial int64) *Budget {
	b := &Budget{}
	b.remaining.Store(initial)
	return b
}

// Take deducts n and reports whether budget remained at or above zero AFTER the deduction.
func (b *Budget) Take(n int64) bool {
	if b == nil {
		return true
	}
	left := b.remaining.Add(-n)
	return left >= 0
}

// Remaining returns the current budget. May be negative if Take overshot.
func (b *Budget) Remaining() int64 {
	if b == nil {
		return 0
	}
	return b.remaining.Load()
}
