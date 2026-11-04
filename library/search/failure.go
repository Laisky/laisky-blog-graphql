package search

import (
	"context"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

const failureStageFetch = "fetch"
const failureCodeFetch = "fetch_failed"
const failureCodeCrawler = "crawler_failed"
const failureStageRender = "render"
const fetchDiagnosticTextLimit = 2048
const fetchLifecycleUnknown = "unknown"

// FetchFailureDiagnostic contains reviewed scalar fields safe for remote alerts.
type FetchFailureDiagnostic struct {
	ErrorCode, Stage, TaskID    string
	WorkerStage, WorkerCategory string
	WorkerSignal                string
	WorkerContext               *crawleregress.FailureContext `json:"WorkerContext,omitempty"`
}

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
	case "admission", "submit", "result", "verification", failureStageRender, "wait", failureStageFetch:
	default:
		diagnostic.Stage = failureStageFetch
	}
	if diagnostic.ErrorCode == failureCodeCrawler && diagnostic.Stage == failureStageRender {
		diagnostic.WorkerStage, diagnostic.WorkerCategory = crawleregress.WorkerFailureDiagnostic(diagnostic.WorkerStage, diagnostic.WorkerCategory)
		if diagnostic.WorkerStage != "" {
			diagnostic.WorkerContext = crawleregress.ReviewedFailureContext(diagnostic.WorkerContext)
		} else {
			diagnostic.WorkerContext = nil
		}
		if diagnostic.TaskID != "" {
			diagnostic.WorkerSignal = crawleregress.WorkerChallengeSignal(diagnostic.WorkerStage, diagnostic.WorkerCategory, diagnostic.WorkerSignal)
		} else {
			diagnostic.WorkerSignal = ""
		}
	} else {
		diagnostic.WorkerStage, diagnostic.WorkerCategory, diagnostic.WorkerSignal = "", "", ""
		diagnostic.WorkerContext = nil
	}
	return diagnostic
}

// FetchCallerContextState reports only the caller context state at observation time.
// An active caller does not exclude a separate worker or dependency cancellation.
func FetchCallerContextState(ctx context.Context) string {
	switch ctx.Err() {
	case context.Canceled:
		return "canceled" //nolint:misspell // stable diagnostic enum
	case context.DeadlineExceeded:
		return "deadline_exceeded"
	default:
		return "active"
	}
}

// FetchErrorLifecycle reports bounded error-chain composition without attributing its source.
// Mixed means a lifecycle sentinel joined with a different lifecycle or concrete cause.
// Unknown means the traversal budget was exceeded; existing error codes are unchanged.
func FetchErrorLifecycle(err error) string {
	if err == nil {
		return "none"
	}
	pending := []error{err}
	canceled, deadline, concrete := false, false, false
	for inspected := 0; len(pending) > 0; inspected++ {
		if inspected >= 32 {
			return fetchLifecycleUnknown
		}
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if node == nil {
			continue
		}
		switch node {
		case context.Canceled: //nolint:errorlint // Direct-node identity keeps recursive inspection within the explicit budget.
			canceled = true
			continue
		case context.DeadlineExceeded:
			deadline = true
			continue
		}
		switch cause := node.(type) { //nolint:errorlint // Inspect direct unwrap edges within the explicit traversal budget.
		case interface{ Unwrap() []error }:
			children := cause.Unwrap()
			if len(children)+len(pending) > 32 {
				return fetchLifecycleUnknown
			}
			pending = append(pending, children...)
		case interface{ Unwrap() error }:
			child := cause.Unwrap()
			if child == nil {
				concrete = true
			} else {
				pending = append(pending, child)
			}
		default:
			concrete = true
		}
	}
	switch {
	case (canceled && deadline) || ((canceled || deadline) && concrete):
		return "mixed"
	case canceled:
		return "canceled" //nolint:misspell // stable diagnostic enum
	case deadline:
		return "deadline_exceeded"
	default:
		return "none"
	}
}

// FetchLogError keeps useful sanitized cause text while retaining the original error separately.
// The presentation error never unwraps to a raw cause or exports its verbose stack.
func FetchLogError(err error) error {
	if err == nil {
		return nil
	}
	diagnostic := FetchDiagnostic(err)
	cause := err
	var failure *FetchFailureError
	if errors.As(err, &failure) {
		cause = failure.Cause
	}
	text := ""
	if cause != nil {
		text = safeFetchCauseText(cause)
	}
	return &fetchLogError{diagnostic: diagnostic, text: text}
}

type fetchLogError struct {
	diagnostic FetchFailureDiagnostic
	text       string
}

func (e *fetchLogError) Error() string {
	prefix := e.diagnostic.ErrorCode + ": " + e.diagnostic.Stage
	if e.text == "" {
		return prefix
	}
	return prefix + ": " + e.text
}

func safeFetchCauseText(cause error) (text string) {
	defer func() {
		if recover() != nil {
			text = "[error text unavailable]"
		}
	}()
	text = logSDK.SanitizeDiagnosticText(cause.Error())
	if len(text) > fetchDiagnosticTextLimit {
		return "[diagnostic text exceeds limit]"
	}
	return text
}

type fetchLoggerKey struct{}

// WithFetchLogger threads a reviewed application logger rather than framework request fields.
func WithFetchLogger(ctx context.Context, logger logSDK.Logger) context.Context {
	return context.WithValue(ctx, fetchLoggerKey{}, logger)
}

func reviewedFetchLogger(ctx context.Context, fallback logSDK.Logger) logSDK.Logger {
	if logger, ok := ctx.Value(fetchLoggerKey{}).(logSDK.Logger); ok && logger != nil {
		return logger
	}
	return fallback
}
func fetchFailure(err error, code, stage, taskID string) error {
	return errors.WithStack(&FetchFailureError{Diagnostic: FetchFailureDiagnostic{ErrorCode: code, Stage: stage, TaskID: safeTaskID(taskID)}, Cause: err})
}
func safeTaskID(taskID string) string {
	parsed, err := uuid.Parse(taskID)
	if err != nil || len(taskID) != 36 || parsed == uuid.Nil || parsed.String() != taskID {
		return ""
	}
	return taskID
}
func knownFailureCode(code string) bool {
	switch code {
	case failureCodeFetch, "fetch_admission_failed", "crawler_submit_failed", "crawler_result_failed",
		"crawler_egress_unverified", "crawler_egress_rejected", failureCodeCrawler, "crawler_status_invalid",
		"fetch_canceled", "fetch_timeout": //nolint:misspell // stable externally consumed diagnostic enum
		return true
	default:
		return false
	}
}
