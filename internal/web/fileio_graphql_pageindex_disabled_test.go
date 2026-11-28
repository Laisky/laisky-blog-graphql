package web

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// TestGraphQLPageIndexSelectionIsDisabled verifies that MemoryPlugin.PAGEINDEX
// stays accepted on the wire but fails with the shared INVALID_ARGUMENT
// contract before any backend call, while the default selection still works.
func TestGraphQLPageIndexSelectionIsDisabled(t *testing.T) {
	plugin := newRecordingPlugin()
	manager, err := mcpplugin.NewManager(plugin.Name(), plugin)
	require.NoError(t, err)
	resolver := newContractResolver(ResolverArgs{MCPFileService: manager})

	for _, operation := range []string{
		`query { FileRead(project:"p",path:"/a",plugin:PAGEINDEX) { content } }`,
		`mutation { FileRename(project:"p",from_path:"/a.md",to_path:"/a.pdf",` +
			`expected_version:"0123456789abcdef0123456789abcdef:1",plugin:PAGEINDEX) { moved_count } }`,
	} {
		response := execute(t, resolver, true, operation)
		require.Equal(t, string(files.ErrCodeInvalidArgument), errorCode(t, response), operation)
		require.Equal(t, mcpplugin.PageIndexDisabledMessage, response.Errors[0].Message)
		require.Equal(t, false, response.Errors[0].Extensions["retryable"])
		require.Equal(t, []any{plugin.Name()}, response.Errors[0].Extensions["available_plugins"])
	}
	require.Zero(t, plugin.attempts, "a disabled selection never reaches the backend")
	require.Zero(t, plugin.reads)
	require.Zero(t, plugin.renames)

	response := execute(t, resolver, true, `query { FileRead(project:"p",path:"/a") { content } }`)
	require.Empty(t, response.Errors)
	require.Equal(t, 1, plugin.reads)
}
