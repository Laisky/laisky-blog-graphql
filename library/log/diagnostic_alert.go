package log

import "github.com/Laisky/zap/zapcore"

// WebDiagnosticAlertHook forwards default diagnostic fields only from reviewed
// operation loggers, plus the sealed content-free MCP failure envelope. Other
// services can log user data; their previous metadata-only
// alert export stays intact. Reviewed web-operation fields remain faithful; the
// MCP branch projects only its source-authored sealed envelope.
func WebDiagnosticAlertHook(hook func(zapcore.Entry, []zapcore.Field) error) func(zapcore.Entry, []zapcore.Field) error {
	return func(entry zapcore.Entry, fields []zapcore.Field) error {
		if reviewedWebDiagnosticScope(entry.LoggerName) {
			return hook(entry, fields)
		}
		return hook(entry, mcpFailureAlertFields(entry, fields))
	}
}
