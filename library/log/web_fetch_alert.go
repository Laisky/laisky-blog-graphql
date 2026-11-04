package log

import (
	"strings"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// WebFetchFailureSummary is the reviewed scalar envelope for one fetch failure.
// Error details, target URLs and user content must never be added to it.
type WebFetchFailureSummary struct {
	CallerContextState, ErrorLifecycle  string
	ErrorCode, Stage, RequestID, TaskID string
	WorkerStage, WorkerCategory         string
	WorkerSignal                        string
	DurationMS                          int64
	TargetScheme, TargetHostname        string
	WorkerContext                       *crawleregress.FailureContext
}

const (
	webFetchErrorCrawler      = "crawler_failed"
	webFetchStageFetch        = "fetch"
	webFetchStageResult       = "result"
	webFetchStageVerification = "verification"
	webFetchStageWait         = "wait"
	webFetchStageRender       = "render"
)

var webFetchFailureStages = map[string]string{
	"fetch_failed":              webFetchStageFetch,
	"fetch_admission_failed":    "admission",
	"crawler_submit_failed":     "submit",
	"crawler_result_failed":     webFetchStageResult,
	"crawler_egress_unverified": webFetchStageVerification,
	"crawler_egress_rejected":   webFetchStageVerification,
	webFetchErrorCrawler:        webFetchStageRender,
	"crawler_status_invalid":    webFetchStageResult,
	"fetch_canceled":            webFetchStageWait,
	"fetch_timeout":             webFetchStageWait,
}

// WebFetchFailureFields returns dedicated fields for the reviewed alert callsite.
// It normalizes unknown enums, rejects noncanonical correlation IDs and bounds
// duration. Dedicated names prevent unrelated generic field collisions.
func WebFetchFailureFields(summary WebFetchFailureSummary) []zapcore.Field {
	stage, ok := webFetchFailureStages[summary.ErrorCode]
	if summary.ErrorCode == "fetch_canceled" || summary.ErrorCode == "fetch_timeout" {
		switch summary.Stage {
		case "admission", "submit", webFetchStageResult, webFetchStageVerification, webFetchStageRender, webFetchStageWait, webFetchStageFetch:
			stage = summary.Stage
		}
	}
	if !ok || stage != summary.Stage {
		summary.ErrorCode, summary.Stage = "fetch_failed", webFetchStageFetch
	}
	fields := []zapcore.Field{
		zap.String("web_fetch_error_code", summary.ErrorCode),
		zap.String("web_fetch_stage", summary.Stage),
	}
	if summary.ErrorCode == webFetchErrorCrawler && summary.Stage == webFetchStageRender {
		workerStage, workerCategory := crawleregress.WorkerFailureDiagnostic(summary.WorkerStage, summary.WorkerCategory)
		if workerStage != "" {
			fields = append(fields, zap.String("web_fetch_worker_stage", workerStage), zap.String("web_fetch_worker_category", workerCategory))
			if signal := crawleregress.WorkerChallengeSignal(workerStage, workerCategory, summary.WorkerSignal); signal != "" && canonicalCorrelationID(summary.TaskID) {
				fields = append(fields, zap.String("web_fetch_worker_signal", signal))
			}
		}
	}
	switch summary.CallerContextState {
	case "active", "canceled", "deadline_exceeded": //nolint:misspell // stable diagnostic enum
		fields = append(fields, zap.String("web_fetch_caller_context_state", summary.CallerContextState))
	}
	switch summary.ErrorLifecycle {
	case "none", "canceled", "deadline_exceeded", "mixed", "unknown": //nolint:misspell // stable diagnostic enum
		fields = append(fields, zap.String("web_fetch_error_lifecycle", summary.ErrorLifecycle))
	}
	if canonicalCorrelationID(summary.RequestID) {
		fields = append(fields, zap.String("web_fetch_request_id", summary.RequestID))
	}
	if canonicalCorrelationID(summary.TaskID) {
		fields = append(fields, zap.String("web_fetch_task_id", summary.TaskID))
	}
	if summary.DurationMS >= 0 && summary.DurationMS <= 24*60*60*1000 {
		fields = append(fields, zap.Int64("web_fetch_duration_ms", summary.DurationMS))
	}
	return fields
}

// WebFetchDiagnosticFields supplies backward-compatible authored scalar names.
// SDK default delivery encodes chosen fields faithfully and rejects an oversized complete record.
func WebFetchDiagnosticFields(summary WebFetchFailureSummary) []zapcore.Field {
	fields := WebFetchFailureFields(summary)
	for i := range fields {
		fields[i].Key = strings.TrimPrefix(fields[i].Key, "web_fetch_")
	}
	return append(fields, webFetchContextFields(summary, fields)...)
}

func canonicalCorrelationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// WebFetchAlertOption retains the optional legacy explicit-filter policy.
// Production uses the app-scoped faithful SDK core instead. Callers
// choosing this adapter pair it with WebFetchAlertHook for prefix validation.
// Its unchanged field list omits worker_signal; production diagnostic scopes
// carry the bounded field without this optional adapter.
func WebFetchAlertOption() logSDK.AlertOption {
	return logSDK.WithAlertFieldAllowlist("error_code", "stage", "request_id", "task_id", "duration_ms", "worker_stage", "worker_category")
}

// WebFetchAlertHook is an optional legacy explicit-filter adapter. It retains
// metadata-only selection outside the fixed fetch failure callsite and validates
// dedicated scalar fields without invoking error/object marshalers.
func WebFetchAlertHook(alert *logSDK.Alert) func(zapcore.Entry, []zapcore.Field) error {
	core := alert.GetZapCore()
	hook := func(entry zapcore.Entry, fields []zapcore.Field) error {
		if !core.Enabled(entry.Level) {
			return nil
		}
		return core.Write(entry, fields)
	}
	return func(entry zapcore.Entry, fields []zapcore.Field) error {
		if entry.Message != "web_fetch failed" || !strings.HasSuffix(entry.LoggerName, ".web_fetch") {
			return hook(entry, nil)
		}
		summary := WebFetchFailureSummary{DurationMS: -1}
		seen := map[string]bool{}
		for _, field := range fields {
			if field.Type == zapcore.NamespaceType {
				break
			}
			if !strings.HasPrefix(field.Key, "web_fetch_") {
				continue
			}
			if seen[field.Key] {
				return hook(entry, nil)
			}
			seen[field.Key] = true
			applyFetchAlertField(&summary, field)
		}
		approved := WebFetchFailureFields(summary)
		for i := range approved {
			approved[i].Key = strings.TrimPrefix(approved[i].Key, "web_fetch_")
		}
		return hook(entry, approved)
	}
}

func applyFetchAlertField(summary *WebFetchFailureSummary, field zapcore.Field) {
	if field.Type == zapcore.StringType {
		switch field.Key {
		case "web_fetch_error_code":
			summary.ErrorCode = field.String
		case "web_fetch_stage":
			summary.Stage = field.String
		case "web_fetch_worker_stage":
			summary.WorkerStage = field.String
		case "web_fetch_worker_category":
			summary.WorkerCategory = field.String
		case "web_fetch_request_id":
			summary.RequestID = field.String
		case "web_fetch_task_id":
			summary.TaskID = field.String
		}
	} else if field.Key == "web_fetch_duration_ms" && field.Type == zapcore.Int64Type {
		summary.DurationMS = field.Integer
	}
}
