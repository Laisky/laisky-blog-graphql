package log

import (
	"github.com/Laisky/zap/zapcore"
	"go.opentelemetry.io/otel/attribute"
)

// projectOTLPContent maps an ordinary entry without inherited context using native OTLP values.
func projectOTLPContent(entry zapcore.Entry, fields []zapcore.Field) ([]attribute.KeyValue, error) {
	return projectOTLPContext(entry, fields, nil)
}

// projectOTLPContext extends an immutable branch snapshot with callsite fields in source order.
func projectOTLPContext(entry zapcore.Entry, fields []zapcore.Field, inherited *otlpObjectEncoder) ([]attribute.KeyValue, error) {
	encoder := newOTLPObjectEncoder()
	if inherited != nil {
		encoder = inherited.clone()
	}
	encoder.addFields(fields)
	attrs, err := encoder.attributes()
	if err != nil {
		return nil, err
	}
	add := func(key string, value attribute.Value) {
		for _, field := range attrs {
			if string(field.Key) == key {
				return
			}
		}
		attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(key), Value: value})
	}
	if entry.LoggerName != "" {
		add("logger.name", otlpStringValue(entry.LoggerName))
	}
	if entry.Caller.Defined {
		add("code.file.path", otlpStringValue(entry.Caller.File))
		add("code.line.number", attribute.IntValue(entry.Caller.Line))
	}
	if entry.Stack != "" {
		add("exception.stacktrace", otlpStringValue(entry.Stack))
	}
	return attrs, nil
}
