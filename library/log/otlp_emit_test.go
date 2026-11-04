package log

import "github.com/Laisky/zap/zapcore"

// emit exports one record without an inherited With snapshot for direct runtime tests.
func (r *otlpRuntime) emit(entry zapcore.Entry, fields []zapcore.Field) error {
	return r.emitContext(entry, fields, nil)
}
