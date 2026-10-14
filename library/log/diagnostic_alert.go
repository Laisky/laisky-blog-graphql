package log

import "github.com/Laisky/zap/zapcore"

// WebDiagnosticAlertHook forwards default diagnostic fields only from reviewed
// operation loggers. Other services can log user data; their previous metadata-only
// alert export stays intact. No diagnostic field names are selected or discarded.
func WebDiagnosticAlertHook(hook func(zapcore.Entry, []zapcore.Field) error) func(zapcore.Entry, []zapcore.Field) error {
	return func(entry zapcore.Entry, fields []zapcore.Field) error {
		switch entry.LoggerName {
		case "graphql.laisky-graphql.mcp.web_search",
			"graphql.laisky-graphql.mcp.web_fetch",
			"graphql.laisky-graphql.mcp.web_fetch.fetch_dynamic_url_content":
			return hook(entry, fields)
		default:
			return hook(entry, nil)
		}
	}
}
