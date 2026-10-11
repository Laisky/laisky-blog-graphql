package log

import (
	"strings"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/google/uuid"
)

// WebFetchFailureSummary is the reviewed scalar envelope for one fetch failure.
// Error details, target URLs and user content must never be added to it.
type WebFetchFailureSummary struct {
	ErrorCode, Stage, RequestID, TaskID string
	DurationMS                          int64
}

const (
	webFetchStageFetch        = "fetch"
	webFetchStageResult       = "result"
	webFetchStageVerification = "verification"
	webFetchStageWait         = "wait"
)

var webFetchFailureStages = map[string]string{
	"fetch_failed":              webFetchStageFetch,
	"fetch_admission_failed":    "admission",
	"crawler_submit_failed":     "submit",
	"crawler_result_failed":     webFetchStageResult,
	"crawler_egress_unverified": webFetchStageVerification,
	"crawler_egress_rejected":   webFetchStageVerification,
	"crawler_failed":            "render",
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
		case "admission", "submit", webFetchStageResult, webFetchStageVerification, "render", webFetchStageWait, webFetchStageFetch:
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

func canonicalCorrelationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// WebFetchAlertOption allows only the keys emitted by WebFetchAlertHook.
// These options must be used together with that hook; directly installing the
// SDK hook would allow unreviewed generic fields from unrelated callsites.
func WebFetchAlertOption() logSDK.AlertOption {
	return logSDK.WithAlertFieldAllowlist("error_code", "stage", "request_id", "task_id", "duration_ms")
}

// WebFetchAlertHook keeps all existing alerts metadata-only except the fixed
// web_fetch failure callsite. It validates dedicated scalar fields before
// handing them to the SDK allowlist, without invoking error/object marshalers.
func WebFetchAlertHook(alert *logSDK.Alert) func(zapcore.Entry, []zapcore.Field) error {
	hook := alert.GetZapHook()
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
			if field.Type == zapcore.StringType {
				switch field.Key {
				case "web_fetch_error_code":
					summary.ErrorCode = field.String
				case "web_fetch_stage":
					summary.Stage = field.String
				case "web_fetch_request_id":
					summary.RequestID = field.String
				case "web_fetch_task_id":
					summary.TaskID = field.String
				}
			} else if field.Key == "web_fetch_duration_ms" && field.Type == zapcore.Int64Type {
				summary.DurationMS = field.Integer
			}
		}
		approved := WebFetchFailureFields(summary)
		for i := range approved {
			approved[i].Key = strings.TrimPrefix(approved[i].Key, "web_fetch_")
		}
		return hook(entry, approved)
	}
}
