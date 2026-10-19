package telegram

import "fmt"

// AlertFanoutError reports provider outcomes without claiming atomic or exactly-once delivery.
// Retrying the complete mutation can duplicate recipients already acknowledged by Telegram.
type AlertFanoutError struct {
	Total             int
	Attempted         int
	Accepted          int
	Rejected          int
	Unconfirmed       int
	Unattempted       int
	RetryAfterSeconds int
	cause             error
}

// Error returns safe aggregate counts, never application content, tokens or raw API errors.
func (e *AlertFanoutError) Error() string {
	return fmt.Sprintf("telegram alert fanout incomplete: total=%d attempted=%d accepted=%d rejected=%d unconfirmed=%d unattempted=%d retry_after=%d seconds; retry may duplicate accepted sends",
		e.Total, e.Attempted, e.Accepted, e.Rejected, e.Unconfirmed, e.Unattempted, e.RetryAfterSeconds)
}

// Unwrap exposes request cancellation or deadline without retaining unsafe transport errors.
func (e *AlertFanoutError) Unwrap() error { return e.cause }
