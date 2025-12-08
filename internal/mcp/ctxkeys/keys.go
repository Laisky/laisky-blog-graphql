package ctxkeys

// Key identifies a context value propagated across MCP services.
type Key string

const (
	// Logger stores the per-request logger within tool contexts.
	Logger Key = "mcp_logger"
)
