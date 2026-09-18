package oneapi

import (
	"testing"

	"github.com/Laisky/errors/v2"
)

// TestClassifyBillingOutcomeRejectsInvalidTypedStates protects the audit boundary:
// an unknown enum is not evidence that a consume did not happen.
func TestClassifyBillingOutcomeRejectsInvalidTypedStates(t *testing.T) {
	for _, outcome := range []BillingOutcome{"", "ACCEPTED", "rejected", "future_state"} {
		for _, wrapped := range []bool{false, true} {
			t.Run(string(outcome)+map[bool]string{false: "/direct", true: "/wrapped"}[wrapped], func(t *testing.T) {
				var err error = &BillingError{Outcome: outcome, Reason: "synthetic boundary input"}
				if wrapped {
					err = errors.Wrap(err, "consume")
				}
				got := ClassifyBillingOutcome(err)
				if got != BillingUnknown || !got.Charged() || !got.Indeterminate() || got.SafeToRetry() {
					t.Fatalf("invalid outcome %q became %q: charged=%v indeterminate=%v safe_to_retry=%v",
						outcome, got, got.Charged(), got.Indeterminate(), got.SafeToRetry())
				}
			})
		}
	}
}

// TestClassifyBillingOutcomePreservesDefinedStates prevents hardening from
// relabelling an explicit denial, accepted consume, or never-sent request.
func TestClassifyBillingOutcomePreservesDefinedStates(t *testing.T) {
	for _, outcome := range []BillingOutcome{BillingAccepted, BillingDenied, BillingUnknown, BillingNotAttempted} {
		t.Run(string(outcome), func(t *testing.T) {
			for _, err := range []error{&BillingError{Outcome: outcome}, errors.Wrap(&BillingError{Outcome: outcome}, "consume")} {
				if got := ClassifyBillingOutcome(err); got != outcome {
					t.Fatalf("want %q, got %q", outcome, got)
				}
			}
		})
	}
	if got := ClassifyBillingOutcome(nil); got != BillingAccepted {
		t.Fatalf("nil error must remain accepted, got %q", got)
	}
	var typedNil *BillingError
	for _, err := range []error{typedNil, errors.Wrap(typedNil, "consume"), errors.New("unclassified transport error")} {
		if got := ClassifyBillingOutcome(err); got != BillingUnknown {
			t.Fatalf("unclassified failure must remain unknown, got %q", got)
		}
	}
}
