package files_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	pageindex "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// TestFileIOPluginConditionalLifecycle checks both actual routing paths; PageIndex
// uses AtomicSystemFS instead of the ordinary Delete entry point.
func TestFileIOPluginConditionalLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, name := range []string{"rag", "pageindex"} {
			t.Run(backend+"/"+name, func(t *testing.T) {
				f := newRaceFixture(t, backend, nil)
				var plug mcpplugin.Plugin
				if name == "rag" {
					p, err := ragplugin.New(f.svc[0])
					require.NoError(t, err)
					plug = p
				} else {
					fs, err := f.svc[0].SystemNamespace("pageindex")
					require.NoError(t, err)
					p, err := pageindex.New(pageindex.PluginDeps{UserFS: f.svc[0], SystemFS: fs})
					require.NoError(t, err)
					plug = p
				}
				manager, err := mcpplugin.NewManager(name, plug)
				require.NoError(t, err)
				deleter, err := tools.NewFileDeleteTool(manager)
				require.NoError(t, err)
				renamer, err := tools.NewFileRenameTool(manager)
				require.NoError(t, err)
				ctx := context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)
				f.write(t, 0, "/a.txt", "A", files.WriteModeTruncate, 0)
				old := readRaceVersion(t, f, "/a.txt")
				f.write(t, 1, "/a.txt", "B", files.WriteModeTruncate, 0)
				current := readRaceVersion(t, f, "/a.txt")
				jobs, snapshots := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
				args := map[string]any{"project": "race", "path": "a.txt", "expected_version": old.Version}
				require.Equal(t, "VERSION_CONFLICT", callRaceToolError(t, ctx, deleter, "file_delete", args)["code"])
				require.Equal(t, current, readRaceVersion(t, f, "/a.txt"))
				move := map[string]any{"project": "race", "from_path": "a.txt", "to_path": "b.txt", "expected_version": old.Version}
				require.Equal(t, "VERSION_CONFLICT", callRaceToolError(t, ctx, renamer, "file_rename", move)["code"])
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				require.Equal(t, snapshots, f.count(t, "mcp_file_versions"))
				move["expected_version"] = current.Version
				callRaceTool(t, ctx, renamer, "file_rename", move)
				after := readRaceVersion(t, f, "/b.txt")
				require.Equal(t, "B", after.Content)
				require.NotEqual(t, current.Version, after.Version)
				args["path"], args["expected_version"] = "b.txt", after.Version
				callRaceTool(t, ctx, deleter, "file_delete", args)
				stat, err := f.svc[0].Stat(f.ctx, f.auth, "race", "/b.txt")
				require.NoError(t, err)
				require.False(t, stat.Exists)
			})
		}
	}
}

// legacyFilePlugin deliberately hides the optional capability marker while
// forwarding the old interface, modeling an adapter that cannot honor conditions.
type legacyFilePlugin struct{ mcpplugin.Plugin }

// TestFileIOUnsupportedConditionBackend fails closed rather than silently writing.
func TestFileIOUnsupportedConditionBackend(t *testing.T) {
	f := newRaceFixture(t, "sqlite", nil)
	plug, err := ragplugin.New(f.svc[0])
	require.NoError(t, err)
	writer, err := tools.NewFileWriteTool(legacyFilePlugin{Plugin: plug})
	require.NoError(t, err)
	ctx := context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)
	args := map[string]any{"project": "race", "path": "a.txt", "content": "A", "create_only": true}
	require.Equal(t, "INVALID_ARGUMENT", callRaceToolError(t, ctx, writer, "file_write", args)["code"])
	require.Zero(t, f.count(t, "mcp_files"))
	require.Zero(t, f.count(t, "mcp_file_index_jobs"))
}
