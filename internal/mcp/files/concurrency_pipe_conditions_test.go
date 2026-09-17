package files_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestFileIOMCPPipeRetainsVersionConditions exercises the actual pipeline and
// FileIO handlers. A pipeline is not an exception to the external CAS contract.
func TestFileIOMCPPipeRetainsVersionConditions(t *testing.T) {
	f := newEntrypointFixture(t, "sqlite", "rag")
	callRaceTool(t, f.toolCtx, f.writer, "file_write", map[string]any{
		"project": "race", "path": "/piped.txt", "content": "base", "mode": "TRUNCATE", "create_only": true,
	})
	_, err := f.db[0].ExecContext(f.ctx, `UPDATE mcp_files SET revision=9007199254740993
		WHERE apikey_hash=? AND project=? AND path=? AND system_owner=?`, f.auth.APIKeyHash, "race", "/piped.txt", "")
	require.NoError(t, err)
	pipe, err := tools.NewMCPPipeTool(log.Logger, func(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
		request := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}}
		if name == "file_read" {
			return f.reader.Handle(ctx, request)
		}
		return f.writer.Handle(ctx, request)
	}, tools.PipeLimits{})
	require.NoError(t, err)
	write := func(id, content string) map[string]any {
		return map[string]any{"id": id, "tool": "file_write", "args": map[string]any{
			"project": "race", "path": "/piped.txt", "mode": "TRUNCATE", "content": content,
			"expected_version": map[string]any{"$ref": "steps.base.structured.version"},
		}}
	}
	result, err := pipe.Handle(f.toolCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"continue_on_error": false,
		"steps": []any{
			map[string]any{"id": "base", "tool": "file_read", "args": map[string]any{"project": "race", "path": "/piped.txt"}},
			write("first", "accepted"), write("second", "stale pipeline edit"),
		},
	}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(wire), "VERSION_CONFLICT")
	current := readRaceVersion(t, f.raceFixture, "/piped.txt")
	require.Equal(t, "accepted", current.Content)
	require.Contains(t, current.Version, ":9007199254740994")
	require.Equal(t, 1, f.count(t, "mcp_file_versions"))

	result, err = pipe.Handle(f.toolCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"steps": []any{map[string]any{"id": "unguarded", "tool": "file_write", "args": map[string]any{
			"project": "race", "path": "/piped.txt", "content": "missing token", "mode": "TRUNCATE",
		}}},
	}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	wire, err = json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(wire), "PRECONDITION_REQUIRED")
	require.Equal(t, current, readRaceVersion(t, f.raceFixture, "/piped.txt"))
}
