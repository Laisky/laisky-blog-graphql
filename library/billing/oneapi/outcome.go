package oneapi

import (
	"net/http"

	"github.com/Laisky/errors/v2"
)

// BillingOutcome classifies what a consume call actually did at the remote
// billing service.
//
// The distinction exists because the three cases demand opposite handling. A
// denial is definitive: no quota was added, and a later explicit retry is safe.
// An undetermined outcome is not: the consume may already have been applied, so
// it must never be recorded as free, never be presented as a refund, and never
// be replayed automatically.
type BillingOutcome string

const (
	// BillingAccepted means the remote confirmed the quota was added.
	BillingAccepted BillingOutcome = "accepted"
	// BillingDenied means the remote definitively rejected the call and added
	// no quota, for example an invalid key or exhausted balance.
	BillingDenied BillingOutcome = "denied"
	// BillingUnknown means the remote state is undetermined: a timeout, a
	// transport failure, or a server-side error that may have applied the
	// consume before failing.
	BillingUnknown BillingOutcome = "unknown"
	// BillingNotAttempted means no request was sent, so nothing was charged.
	BillingNotAttempted BillingOutcome = "not_attempted"
)

// Charged reports whether the configured price must be recorded as possibly
// posted. An undetermined outcome counts as charged, because presenting it as
// free would understate a charge that may exist.
func (o BillingOutcome) Charged() bool {
	return o == BillingAccepted || o == BillingUnknown
}

// Indeterminate reports whether the remote result is unresolved, which means an
// audit row for it is not a receipt and requires manual reconciliation.
func (o BillingOutcome) Indeterminate() bool { return o == BillingUnknown }

// SafeToRetry reports whether a later explicit action may repeat the call
// without risking a double charge. An accepted call needs no retry, and an
// undetermined one must not be replayed automatically.
func (o BillingOutcome) SafeToRetry() bool {
	return o == BillingDenied || o == BillingNotAttempted
}

// BillingError carries the classified outcome of a consume attempt.
//
// It deliberately does not include the API key or the raw remote body beyond a
// short reason, so an audit row or log line cannot leak a credential.
type BillingError struct {
	// Outcome is the classification a caller must branch on.
	Outcome BillingOutcome
	// Status is the remote HTTP status, or zero when no response arrived.
	Status int
	// Reason is a bounded description for logs and audit rows.
	Reason string
}

// Error implements the error interface without echoing credentials.
func (e *BillingError) Error() string {
	if e == nil {
		return "billing error: <nil>"
	}
	if e.Reason == "" {
		return "billing " + string(e.Outcome)
	}
	return "billing " + string(e.Outcome) + ": " + e.Reason
}

// ClassifyBillingOutcome extracts the outcome from a consume error.
//
// An error without a recognized classification, including an invalid typed
// outcome, resolves to BillingUnknown rather than BillingDenied. An unrecognized
// failure is not evidence that nothing was charged; treating it as a denial
// would silently understate usage.
func ClassifyBillingOutcome(err error) BillingOutcome {
	if err == nil {
		return BillingAccepted
	}
	var typed *BillingError
	if errors.As(err, &typed) && typed != nil {
		switch typed.Outcome {
		case BillingAccepted, BillingDenied, BillingUnknown, BillingNotAttempted:
			return typed.Outcome
		default:
			// An unknown enum is not evidence that a consume did not happen.
			return BillingUnknown
		}
	}
	return BillingUnknown
}

// billingOutcomeForStatus maps a remote HTTP status onto an outcome.
//
// Client-error statuses are definitive rejections: the request was understood
// and refused, so no quota was added. Server errors and anything unexpected are
// undetermined, because the consume may have been applied before the failure.
func billingOutcomeForStatus(status int) BillingOutcome {
	switch {
	case status == http.StatusOK:
		return BillingAccepted
	case status >= 400 && status < 500:
		return BillingDenied
	default:
		return BillingUnknown
	}
}
