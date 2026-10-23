package log

import (
	"context"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/zap/zapcore"
	otellog "go.opentelemetry.io/otel/log"
)

// otlpHookWithContext emits a callsite with its immutable inherited branch snapshot.
func otlpHookWithContext(entry zapcore.Entry, fields []zapcore.Field, inherited *otlpObjectEncoder) error {
	if runtime := activeOTLP.Load(); runtime != nil {
		return runtime.emitContext(entry, fields, inherited)
	}
	return nil
}

// emitContext projects callbacks before admission so they cannot block bounded SDK shutdown.
func (r *otlpRuntime) emitContext(entry zapcore.Entry, fields []zapcore.Field, inherited *otlpObjectEncoder) error {
	r.admission.RLock()
	stopped := r.stopped
	r.admission.RUnlock()
	if stopped {
		return nil
	}
	attrs, err := projectOTLPContext(entry, fields, inherited)
	if err != nil {
		r.rejected.Add(1)
		return err
	}
	r.admission.RLock()
	defer r.admission.RUnlock()
	if r.stopped {
		r.rejected.Add(1)
		return errors.New("application OTLP pipeline stopped before record admission")
	}
	var record otellog.Record
	record.SetTimestamp(entry.Time)
	record.SetObservedTimestamp(entry.Time)
	record.SetBody(otlpStringValue(entry.Message))
	severity := otellog.Severity(int(entry.Level)*4 + 9)
	if entry.Level > zapcore.DPanicLevel {
		severity = otellog.SeverityFatal1 + otellog.Severity(entry.Level-zapcore.DPanicLevel)
	}
	record.SetSeverity(severity)
	record.SetSeverityText(entry.Level.String())
	record.AddAttributes(attrs...)
	r.emitted.Add(1)
	r.logger.Emit(context.Background(), record)
	return nil
}
