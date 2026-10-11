package search

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
)

const failureStageFetch = "fetch"
const failureCodeFetch = "fetch_failed"

// FetchFailureDiagnostic contains reviewed scalar fields safe for remote alerts.
type FetchFailureDiagnostic struct{ ErrorCode, Stage, TaskID string }

// FetchFailureError carries safe diagnostics while preserving the underlying error identity.
type FetchFailureError struct {
	Diagnostic FetchFailureDiagnostic
	Cause      error
}

// Error returns fixed diagnostic text without URLs, worker failure text or credentials.
func (e *FetchFailureError) Error() string { return e.Diagnostic.ErrorCode + ": " + e.Diagnostic.Stage }

// Unwrap preserves cancellation, timeout and policy error identity.
func (e *FetchFailureError) Unwrap() error { return e.Cause }

// FetchDiagnostic extracts only reviewed bounded scalar diagnostics from an error.
func FetchDiagnostic(err error) FetchFailureDiagnostic {
	diagnostic := FetchFailureDiagnostic{ErrorCode: failureCodeFetch, Stage: failureStageFetch}
	var failure *FetchFailureError
	if errors.As(err, &failure) {
		diagnostic = failure.Diagnostic
	}
	switch {
	case errors.Is(err, context.Canceled):
		diagnostic.ErrorCode = "fetch_canceled" //nolint:misspell // stable externally consumed diagnostic enum
	case errors.Is(err, context.DeadlineExceeded):
		diagnostic.ErrorCode = "fetch_timeout"
	}
	diagnostic.TaskID = safeTaskID(diagnostic.TaskID)
	if !knownFailureCode(diagnostic.ErrorCode) {
		diagnostic.ErrorCode = failureCodeFetch
	}
	switch diagnostic.Stage {
	case "admission", "submit", "result", "verification", "render", "wait", "fetch":
	default:
		diagnostic.Stage = failureStageFetch
	}
	return diagnostic
}
func fetchFailure(err error, code, stage, taskID string) error {
	return errors.WithStack(&FetchFailureError{Diagnostic: FetchFailureDiagnostic{ErrorCode: code, Stage: stage, TaskID: safeTaskID(taskID)}, Cause: err})
}
func safeTaskID(taskID string) string {
	if len(taskID) != 36 {
		return ""
	}
	for index, char := range taskID {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return ""
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return ""
		}
	}
	return taskID
}
func knownFailureCode(code string) bool {
	switch code {
	case failureCodeFetch, "fetch_admission_failed", "crawler_submit_failed", "crawler_result_failed",
		"crawler_egress_unverified", "crawler_egress_rejected", "crawler_failed", "crawler_status_invalid",
		"fetch_canceled", "fetch_timeout": //nolint:misspell // stable externally consumed diagnostic enum
		return true
	default:
		return false
	}
}
