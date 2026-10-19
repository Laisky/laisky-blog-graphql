package log

import (
	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap/zapcore"
)

// webDiagnosticAlertCore preserves the application's existing reviewed-scope field policy.
type webDiagnosticAlertCore struct {
	reviewed zapcore.Core
	metadata zapcore.Core
}

// WebDiagnosticAlertCore routes reviewed operations to a faithful bound-context core.
// Other logger names retain metadata-only alerts. The local logger core is separate.
// Attach this core once in a tee, without an additional alert hook around that tee.
func WebDiagnosticAlertCore(alert *logSDK.Alert) zapcore.Core {
	return &webDiagnosticAlertCore{reviewed: alert.GetZapCore(), metadata: alert.GetZapCore()}
}

// reviewedWebDiagnosticScope retains the exact logger names admitted by the existing hook.
func reviewedWebDiagnosticScope(name string) bool {
	switch name {
	case "graphql.laisky-graphql.mcp.web_search",
		"graphql.laisky-graphql.mcp.web_fetch",
		"graphql.laisky-graphql.mcp.web_fetch.fetch_dynamic_url_content":
		return true
	default:
		return false
	}
}

// Enabled delegates the shared alert-level decision before field serialization.
func (core *webDiagnosticAlertCore) Enabled(level zapcore.Level) bool {
	return core.reviewed.Enabled(level)
}

// With eagerly snapshots reviewed context without binding any fields to metadata-only output.
func (core *webDiagnosticAlertCore) With(fields []zapcore.Field) zapcore.Core {
	return &webDiagnosticAlertCore{reviewed: core.reviewed.With(fields), metadata: core.metadata}
}

// Check registers exactly one scoped writer when the configured alert level permits it.
func (core *webDiagnosticAlertCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checked.AddCore(entry, core)
	}
	return checked
}

// Write forwards one complete entry using the entry's exact application-owned scope.
func (core *webDiagnosticAlertCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if reviewedWebDiagnosticScope(entry.LoggerName) {
		return core.reviewed.Write(entry, fields)
	}
	return core.metadata.Write(entry, nil)
}

// Sync delegates lifecycle semantics; neither SDK core promises asynchronous delivery flushing.
func (core *webDiagnosticAlertCore) Sync() error {
	return errors.WithStack(errors.Join(core.reviewed.Sync(), core.metadata.Sync()))
}

// Fields returns the reviewed immutable snapshot without restoring legacy hook field ordering.
// It is not a scope-aware primitive-field introspection API; scope is known only at Write.
func (core *webDiagnosticAlertCore) Fields() []zapcore.Field {
	return core.reviewed.Fields()
}
