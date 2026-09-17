package files_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// TestFileIOClientPreconditionsMandatory sends real MCP requests through the
// manager and both shipped plugins; it never synthesizes missing caller tokens.
func TestFileIOClientPreconditionsMandatory(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, pluginName := range []string{"rag", "pageindex"} {
			t.Run(backend+"/"+pluginName, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				var plugin mcpplugin.Plugin
				if pluginName == "rag" {
					p, err := ragplugin.New(f.svc[0])
					require.NoError(t, err)
					plugin = p
				} else {
					system, err := f.svc[0].SystemNamespace("pageindex")
					require.NoError(t, err)
					p, err := pageindex.New(pageindex.PluginDeps{UserFS: f.svc[0], SystemFS: system})
					require.NoError(t, err)
					plugin = p
				}
				manager, err := mcpplugin.NewManager(pluginName, plugin)
				require.NoError(t, err)
				writer, err := tools.NewFileWriteTool(manager)
				require.NoError(t, err)
				reader, err := tools.NewFileReadTool(manager)
				require.NoError(t, err)
				deleter, err := tools.NewFileDeleteTool(manager)
				require.NoError(t, err)
				renamer, err := tools.NewFileRenameTool(manager)
				require.NoError(t, err)
				ctx := context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)
				args := map[string]any{"project": "race", "path": "plain.txt", "content": "original", "mode": "TRUNCATE"}
				require.Equal(t, "PRECONDITION_REQUIRED", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
				require.Zero(t, f.count(t, "mcp_files"))
				require.Zero(t, f.count(t, "mcp_file_index_jobs"))

				args["create_only"] = true
				created := callRaceTool(t, ctx, writer, "file_write", args)
				initial := callRaceTool(t, ctx, reader, "file_read", map[string]any{"project": "race", "path": "plain.txt"})
				require.Equal(t, created["version"], initial["version"], "first read needs no precondition")
				before := readRaceVersion(t, f, "/plain.txt")
				rows, history, jobs := f.count(t, "mcp_files"), f.count(t, "mcp_file_versions"), f.count(t, "mcp_file_index_jobs")
				delete(args, "create_only")
				args["content"] = "unprotected edit"
				for _, mode := range []string{"", "APPEND", "OVERWRITE", "TRUNCATE"} {
					args["mode"] = mode
					failure := callRaceToolError(t, ctx, writer, "file_write", args)
					require.Equal(t, "PRECONDITION_REQUIRED", failure["code"])
					require.Equal(t, false, failure["retryable"])
				}
				args["create_only"] = false
				require.Equal(t, "PRECONDITION_REQUIRED", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
				args["create_only"] = nil
				require.Equal(t, "INVALID_ARGUMENT", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
				require.Equal(t, "PRECONDITION_REQUIRED", callRaceToolError(t, ctx, deleter, "file_delete",
					map[string]any{"project": "race", "path": "plain.txt"})["code"])
				for _, target := range []string{"moved.txt", "plain.txt"} {
					require.Equal(t, "PRECONDITION_REQUIRED", callRaceToolError(t, ctx, renamer, "file_rename",
						map[string]any{"project": "race", "from_path": "plain.txt", "to_path": target})["code"])
				}
				require.Equal(t, before, readRaceVersion(t, f, "/plain.txt"))
				require.Equal(t, rows, f.count(t, "mcp_files"))
				require.Equal(t, history, f.count(t, "mcp_file_versions"))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))

				delete(args, "create_only")
				args["expected_version"], args["content"], args["mode"] = before.Version, "accepted", "TRUNCATE"
				accepted := callRaceTool(t, ctx, writer, "file_write", args)
				require.NotEqual(t, before.Version, accepted["version"])
				require.Equal(t, "VERSION_CONFLICT", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
				f.assertStored(t, "/plain.txt", "accepted")
			})
		}
	}
}
