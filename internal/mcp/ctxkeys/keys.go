package ctxkeys

// Key identifies a context value propagated across MCP services.
type Key string

const (
	// Logger stores the per-request logger within tool contexts.
	Logger Key = "mcp_logger"
	// RequestID stores the server-generated correlation ID, never a client header.
	RequestID Key = "mcp_request_id"
	// AuthContext stores the normalized authorization context.
	AuthContext Key = "mcp_auth_context"
)
