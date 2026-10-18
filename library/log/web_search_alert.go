package log

import (
	"strings"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

// WebSearchFailureSummary is the reviewed content-free search alert envelope.
type WebSearchFailureSummary struct {
	Diagnostic diagnostics.Diagnostic
	RequestID  string
	DurationMS int64
}

// WebSearchFailureFields validates dedicated scalar fields for local and remote logs.
func WebSearchFailureFields(summary WebSearchFailureSummary) []zapcore.Field {
	d := diagnostics.Normalize(summary.Diagnostic)
	fields := []zapcore.Field{
		zap.String("web_search_stage", d.Stage), zap.String("web_search_category", d.Category),
		zap.String("web_search_provider", d.Provider), zap.Int("web_search_attempts", d.Attempts),
	}
	if d.Status != 0 {
		fields = append(fields, zap.Int("web_search_status", d.Status))
	}
	if canonicalCorrelationID(summary.RequestID) {
		fields = append(fields, zap.String("web_search_request_id", summary.RequestID))
	}
	if summary.DurationMS >= 0 && summary.DurationMS <= 24*60*60*1000 {
		fields = append(fields, zap.Int64("web_search_duration_ms", summary.DurationMS))
	}
	return fields
}

// WebSearchDiagnosticFields authors one validated scalar set using stable generic aliases.
// Production sends these directly through the SDK's sanitized default hook.
func WebSearchDiagnosticFields(summary WebSearchFailureSummary) []zapcore.Field {
	fields := WebSearchFailureFields(summary)
	for i := range fields {
		fields[i].Key = strings.TrimPrefix(fields[i].Key, "web_search_")
	}
	return fields
}

// ToolFailureAlertOption retains optional legacy explicit field selection.
// Production uses the app-scoped faithful SDK core; this option is only for callers
// deliberately selecting the prefix adapter ToolFailureAlertHook.
func ToolFailureAlertOption() logSDK.AlertOption {
	return logSDK.WithAlertFieldAllowlist("error_code", "stage", "request_id", "task_id", "duration_ms", "category", "provider", "status", "attempts")
}

// ToolFailureAlertHook is the optional legacy prefix-validation adapter.
// It retains explicit selection and metadata-only unrelated events when chosen;
// production installs its separately scoped SDK core.
func ToolFailureAlertHook(alert *logSDK.Alert) func(zapcore.Entry, []zapcore.Field) error {
	fetchHook := WebFetchAlertHook(alert)
	core := alert.GetZapCore()
	hook := func(entry zapcore.Entry, fields []zapcore.Field) error {
		if !core.Enabled(entry.Level) {
			return nil
		}
		return core.Write(entry, fields)
	}
	return func(entry zapcore.Entry, fields []zapcore.Field) error {
		if entry.Message != "web_search failed" || !strings.HasSuffix(entry.LoggerName, ".web_search") {
			return fetchHook(entry, fields)
		}
		summary := WebSearchFailureSummary{DurationMS: -1}
		seen := map[string]bool{}
		for _, field := range fields {
			if field.Type == zapcore.NamespaceType {
				break
			}
			if !strings.HasPrefix(field.Key, "web_search_") {
				continue
			}
			if seen[field.Key] {
				return hook(entry, nil)
			}
			seen[field.Key] = true
			applySearchAlertField(&summary, field)
		}
		approved := WebSearchFailureFields(summary)
		for i := range approved {
			approved[i].Key = strings.TrimPrefix(approved[i].Key, "web_search_")
		}
		return hook(entry, approved)
	}
}

func applySearchAlertField(summary *WebSearchFailureSummary, field zapcore.Field) {
	switch field.Type {
	case zapcore.StringType:
		switch field.Key {
		case "web_search_stage":
			summary.Diagnostic.Stage = field.String
		case "web_search_category":
			summary.Diagnostic.Category = field.String
		case "web_search_provider":
			summary.Diagnostic.Provider = field.String
		case "web_search_request_id":
			summary.RequestID = field.String
		}
	case zapcore.Int64Type:
		switch field.Key {
		case "web_search_status":
			if field.Integer >= 100 && field.Integer <= 599 {
				summary.Diagnostic.Status = int(field.Integer)
			}
		case "web_search_attempts":
			if field.Integer >= 0 && field.Integer <= 100 {
				summary.Diagnostic.Attempts = int(field.Integer)
			}
		case "web_search_duration_ms":
			summary.DurationMS = field.Integer
		}
	case zapcore.UnknownType, zapcore.ArrayMarshalerType, zapcore.ObjectMarshalerType, zapcore.BinaryType,
		zapcore.BoolType, zapcore.ByteStringType, zapcore.Complex128Type, zapcore.Complex64Type, zapcore.DurationType,
		zapcore.Float64Type, zapcore.Float32Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
		zapcore.TimeType, zapcore.TimeFullType, zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type,
		zapcore.Uint8Type, zapcore.UintptrType, zapcore.ReflectType, zapcore.NamespaceType, zapcore.StringerType,
		zapcore.ErrorType, zapcore.SkipType, zapcore.InlineMarshalerType:
		return // Only the two reviewed primitive types above can populate the envelope.
	default:
		return // Future field types remain rejected until reviewed.
	}
}
