package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// APIKeyProvider extracts an API key from the request context.
type APIKeyProvider func(context.Context) string

// AuthorizationHeaderProvider extracts the original authorization header from the request context.
type AuthorizationHeaderProvider func(context.Context) string

// Clock returns the current time. It enables deterministic tests.
type Clock func() time.Time

// Tool exposes the capabilities required by the MCP server registration lifecycle.
type Tool interface {
	Definition() mcp.Tool
	Handle(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}
