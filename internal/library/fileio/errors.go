// Package fileio publishes the shared FileIO and memory services on GraphQL.
//
// GraphQL, MCP and the dedicated HTTP routes are peer interfaces over the same
// application services. This package is an adapter, not a second
// implementation: it reuses the same validation, the same client-precondition
// gate, the same project-selected plugin routing and the same audit tool names
// as the MCP tools, so no interface can diverge into a weaker contract.
package fileio

import (
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// graphQLError maps a typed service error onto a GraphQL error whose extensions
// carry the same machine-stable code and retryability that the MCP tool result
// and the HTTP status mapping expose. gqlgen's default presenter fills in the
// response path, so callers only return the value.
func graphQLError(err error, action string) error {
	if err == nil {
		return nil
	}
	if resolveErr, ok := mcpplugin.AsResolveError(err); ok {
		message := resolveErr.Error()
		if resolveErr.Requested == mcpplugin.DefaultPluginPageIndex && !containsString(resolveErr.Available, mcpplugin.DefaultPluginPageIndex) {
			message += "; settings.mcp.tools.memory.plugins.pageindex.llm.api_key is required"
		}
		return withExtensions(message, map[string]any{
			fieldCode: string(files.ErrCodeInvalidArgument), fieldRetryable: false,
			"available_plugins": resolveErr.Available,
		})
	}
	if typed, ok := files.AsError(err); ok {
		return withExtensions(typed.Message, map[string]any{
			fieldCode: string(typed.Code), fieldRetryable: typed.Retryable,
		})
	}
	if typed, ok := mcpmemory.AsError(err); ok {
		return withExtensions(typed.Message, map[string]any{
			fieldCode: string(typed.Code), fieldRetryable: typed.Retryable,
		})
	}
	// An unclassified failure must not echo internal detail to an external caller.
	return withExtensions("internal error", map[string]any{
		fieldCode: string(files.ErrCodeSearchBackend), fieldRetryable: true, "action": action,
	})
}

// withExtensions builds the GraphQL error carrying the shared error contract.
func withExtensions(message string, extensions map[string]any) error {
	gqlErr := gqlerror.Errorf("%s", message)
	gqlErr.Extensions = extensions
	return gqlErr
}

// invalidArgument reports a caller mistake with the shared INVALID_ARGUMENT code.
func invalidArgument(message string) error {
	return graphQLError(files.NewError(files.ErrCodeInvalidArgument, message, false), "validate argument")
}

// unavailable reports that this deployment has no backend for the operation.
// A missing dependency disables only its own field, never a peer interface.
func unavailable(name string) error {
	return graphQLError(files.NewError(files.ErrCodeSearchBackend, name+" is not available", false), "resolve service")
}

// permissionDenied preserves the shared code for identity failures without
// echoing the rejected header or the underlying parser message.
func permissionDenied() error {
	return graphQLError(files.NewError(files.ErrCodePermissionDenied,
		"missing or invalid authorization", false), "authorize request")
}

// containsString reports whether target appears in values.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
