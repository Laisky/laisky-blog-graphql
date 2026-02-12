package mcp

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestShouldDowngradeMCPErrorLog verifies expected resource-probe errors are downgraded.
func TestShouldDowngradeMCPErrorLog(t *testing.T) {
	require.True(t, shouldDowngradeMCPErrorLog(mcp.MethodResourcesList, requireError("request error: resources not supported")))
	require.True(t, shouldDowngradeMCPErrorLog(mcp.MethodResourcesTemplatesList, requireError("resources not supported")))
}

// TestShouldDowngradeMCPErrorLogFalse verifies unrelated errors remain at error level.
func TestShouldDowngradeMCPErrorLogFalse(t *testing.T) {
	require.False(t, shouldDowngradeMCPErrorLog(mcp.MethodToolsList, requireError("resources not supported")))
	require.False(t, shouldDowngradeMCPErrorLog(mcp.MethodResourcesList, requireError("other failure")))
	require.False(t, shouldDowngradeMCPErrorLog(mcp.MethodResourcesList, nil))
}

// requireError converts text to an error for test readability.
func requireError(msg string) error {
	return &textError{msg: msg}
}

// textError is a lightweight test error implementation.
type textError struct {
	msg string
}

// Error returns the test error message.
func (e *textError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}
