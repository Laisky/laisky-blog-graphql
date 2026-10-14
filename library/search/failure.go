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

// FetchFailureDiagnostic contains reviewed scalar fields safe for remote alerts.
type FetchFailureDiagnostic struct {
	ErrorCode, Stage, TaskID    string
	WorkerStage, WorkerCategory string
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
	} else {
		diagnostic.WorkerStage, diagnostic.WorkerCategory = "", ""
	}
	return diagnostic
}

// FetchLogError exposes only normalized error text, excluding the retained internal cause.
func FetchLogError(err error) error {
	return &FetchFailureError{Diagnostic: FetchDiagnostic(err)}
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
