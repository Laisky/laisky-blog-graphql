package log

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap/zapcore"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
)

const (
	otlpMaxAttributes  = 24
	otlpMaxBodyBytes   = 4096
	otlpMaxStringBytes = 2048
	otlpMaxKeyBytes    = 128
	otlpMaxRecordBytes = 16 * 1024
)

// privateOTLPHook is inherited by loggers created before the private provider exists.
func privateOTLPHook(entry zapcore.Entry, fields []zapcore.Field) error {
	if runtime := activeOTLP.Load(); runtime != nil {
		runtime.emit(entry, fields)
	}
	return nil
}

// emit preserves bounded operational content and applies targeted source-log redaction.
func (r *otlpRuntime) emit(entry zapcore.Entry, fields []zapcore.Field) {
	r.admission.RLock()
	defer r.admission.RUnlock()
	if r.stopped {
		return
	}
	var record otellog.Record
	record.SetTimestamp(entry.Time)
	record.SetObservedTimestamp(entry.Time)
	body, truncated := r.diagnosticText(entry.Message, otlpMaxBodyBytes)
	record.SetBody(attribute.StringValue(body))
	severity := otellog.Severity(int(entry.Level)*4 + 9)
	if entry.Level > zapcore.DPanicLevel {
		severity = otellog.SeverityFatal1 + otellog.Severity(entry.Level-zapcore.DPanicLevel)
	}
	record.SetSeverity(severity)
	record.SetSeverityText(entry.Level.String())
	attrs, dropped, fieldsTruncated := r.diagnosticFields(entry.LoggerName, fields, len(body))
	record.AddAttributes(attrs...)
	if dropped > 0 {
		record.AddAttributes(attribute.Int("app.log.attributes_dropped", dropped))
	}
	if truncated || fieldsTruncated {
		record.AddAttributes(attribute.Bool("app.log.content_truncated", true))
	}
	r.emitted.Add(1)
	r.logger.Emit(context.Background(), record)
}

// diagnosticText reuses the application SDK credential/payload contract and removes bearer echoes.
func (r *otlpRuntime) diagnosticText(text string, limit int) (string, bool) {
	oversized := len(text) > 1<<20
	if !oversized && r.token != "" {
		text = strings.ReplaceAll(text, r.token, "[redacted]")
	}
	text = logSDK.SanitizeDiagnosticText(text)
	if len(text) <= limit {
		return text, oversized
	}
	marker := " [truncated]"
	cut := limit - len(marker)
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return text[:cut] + marker, true
}

// diagnosticFields retains future inline diagnostics without an event or field allowlist.
// Objects, arrays, reflected values and Stringers are not invoked; namespaces stop projection.
func (r *otlpRuntime) diagnosticFields(loggerName string, fields []zapcore.Field, bodyBytes int) ([]attribute.KeyValue, int, bool) {
	attrs := make([]attribute.KeyValue, 0, min(len(fields)+1, otlpMaxAttributes-2))
	seen := make(map[string]bool, otlpMaxAttributes-2)
	remaining := otlpMaxRecordBytes - bodyBytes - 256
	dropped := 0
	truncated := false
	appendValue := func(key string, value attribute.Value) bool {
		size := len(key) + 32
		if value.Type() == attribute.STRING {
			size += len(value.AsString())
		} else {
			size += 16
		}
		if len(attrs) >= otlpMaxAttributes-2 || size > remaining {
			return false
		}
		attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(key), Value: value})
		seen[key] = true
		remaining -= size
		return true
	}
	if loggerName != "" {
		name, cut := r.diagnosticText(loggerName, otlpMaxStringBytes)
		truncated = truncated || cut
		if !appendValue("logger.name", attribute.StringValue(name)) {
			dropped++
		}
	}
	for i, field := range fields {
		if i >= 256 || len(attrs) >= otlpMaxAttributes-2 {
			dropped += len(fields) - i
			break
		}
		if field.Type == zapcore.NamespaceType {
			dropped += len(fields) - i
			break
		}
		if seen[field.Key] {
			dropped++
			continue
		}
		if len(field.Key) == 0 || len(field.Key) > otlpMaxKeyBytes || !utf8.ValidString(field.Key) || sensitiveOTLPKey(field.Key) ||
			reservedOTLPKey(field.Key) {
			dropped++
			continue
		}
		safeKey, cut := r.diagnosticText(field.Key, otlpMaxKeyBytes)
		if cut || safeKey != field.Key || strings.IndexFunc(field.Key, func(ch rune) bool { return ch < 32 || ch == 127 }) >= 0 {
			dropped++
			continue
		}
		value, accepted, valueTruncated := r.diagnosticValue(field)
		truncated = truncated || valueTruncated
		if !accepted || !appendValue(field.Key, value) {
			dropped++
		}
	}
	return attrs, dropped, truncated
}

// diagnosticValue projects supported inline values without arbitrary encoder callbacks.
func (r *otlpRuntime) diagnosticValue(field zapcore.Field) (attribute.Value, bool, bool) {
	switch field.Type {
	case zapcore.StringType:
		text, cut := r.diagnosticText(field.String, otlpMaxStringBytes)
		return attribute.StringValue(text), true, cut
	case zapcore.BoolType:
		return attribute.BoolValue(field.Integer == 1), true, false
	case zapcore.Int8Type, zapcore.Int16Type, zapcore.Int32Type, zapcore.Int64Type:
		return attribute.Int64Value(field.Integer), true, false
	case zapcore.DurationType:
		return attribute.Float64Value(time.Duration(field.Integer).Seconds()), true, false
	case zapcore.TimeType:
		return attribute.StringValue(time.Unix(0, field.Integer).UTC().Format(time.RFC3339Nano)), true, false
	case zapcore.TimeFullType:
		value, ok := field.Interface.(time.Time)
		if !ok {
			return attribute.Value{}, false, false
		}
		return attribute.StringValue(value.UTC().Format(time.RFC3339Nano)), true, false
	case zapcore.Uint8Type, zapcore.Uint16Type, zapcore.Uint32Type, zapcore.Uint64Type:
		if field.Integer < 0 {
			return attribute.StringValue(formatOTLPUint(uint64(field.Integer))), true, false
		}
		return attribute.Int64Value(field.Integer), true, false
	case zapcore.Float64Type:
		value := math.Float64frombits(uint64(field.Integer))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return attribute.Value{}, false, false
		}
		return attribute.Float64Value(value), true, false
	case zapcore.Float32Type:
		value := float64(math.Float32frombits(uint32(field.Integer)))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return attribute.Value{}, false, false
		}
		return attribute.Float64Value(value), true, false
	case zapcore.ErrorType:
		cause, ok := field.Interface.(error)
		if !ok || cause == nil {
			return attribute.Value{}, false, false
		}
		text, cut := r.diagnosticText(otlpErrorText(cause), otlpMaxStringBytes)
		return attribute.StringValue(text), true, cut
	default:
		return attribute.Value{}, false, false
	}
}

// otlpErrorText preserves Error text without formatting stacks or exposing panic values.
func otlpErrorText(cause error) (text string) {
	defer func() {
		if recover() != nil {
			text = "[error text unavailable]"
		}
	}()
	return cause.Error()
}
