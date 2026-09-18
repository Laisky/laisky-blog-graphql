package files_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	pageindex "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

// fixtureHistoryTools uses real RAG/PageIndex adapters and the fixture's shared persistent history.
func fixtureHistoryTools(t *testing.T, f *entrypointFixture, pluginName string) map[string]tools.Tool {
	t.Helper()
	var adapter mcpplugin.Plugin
	var err error
	if pluginName == "pageindex" {
		system, err := f.svc[0].SystemNamespace("pageindex")
		require.NoError(t, err)
		adapter, err = pageindex.New(pageindex.PluginDeps{UserFS: f.svc[0], SystemFS: system})
		require.NoError(t, err)
	} else {
		adapter, err = ragplugin.New(f.svc[0])
		require.NoError(t, err)
	}
	manager, err := mcpplugin.NewManager(pluginName, adapter)
	require.NoError(t, err)
	list, err := tools.NewFileHistoryTools(manager, f.svc[0])
	require.NoError(t, err)
	result := make(map[string]tools.Tool, len(list))
	for _, tool := range list {
		result[tool.Definition().Name] = tool
	}
	return result
}

// TestFileIOHistoryToolsRestoreUsesOriginalLiveVersion checks the real history/read/write pipeline.
func TestFileIOHistoryToolsRestoreUsesOriginalLiveVersion(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, pluginName := range []string{"rag", "pageindex"} {
			t.Run(backend+"/"+pluginName, func(t *testing.T) {
				f := newEntrypointFixture(t, backend, pluginName)
				history := fixtureHistoryTools(t, f, pluginName)
				path := "/restore.txt"
				f.write(t, 0, path, "original 漢🙂", files.WriteModeTruncate, 0)
				f.write(t, 1, path, "second", files.WriteModeTruncate, 0)
				base := readRaceVersion(t, f.raceFixture, path)
				args := map[string]any{"project": "race", "path": path}
				listed := callRaceTool(t, f.toolCtx, history[tools.FileListVersionsToolName], tools.FileListVersionsToolName, args)
				rows := listed["versions"].([]any)
				require.Len(t, rows, 1)
				id := rows[0].(map[string]any)["id"].(string)
				args["history_id"] = id
				snapshot := callRaceTool(t, f.toolCtx, history[tools.FileReadVersionToolName], tools.FileReadVersionToolName, args)
				require.Equal(t, "original 漢🙂", snapshot["content"])
				require.Equal(t, "utf-8", snapshot["content_encoding"])
				require.Equal(t, id, snapshot["history_id"])
				require.NotContains(t, snapshot, "version", "a history ID is not a live token")

				// Another connection changes the file after the client chose its restore base.
				f.write(t, 1, path, "concurrent third", files.WriteModeTruncate, 0)
				current := readRaceVersion(t, f.raceFixture, path)
				jobs, snapshots := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
				args["expected_version"] = base.Version
				failure := callRaceToolError(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
				require.Equal(t, "VERSION_CONFLICT", failure["code"])
				require.Equal(t, current, readRaceVersion(t, f.raceFixture, path))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				require.Equal(t, snapshots, f.count(t, "mcp_file_versions"))

				delete(args, "expected_version")
				failure = callRaceToolError(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
				require.Equal(t, "PRECONDITION_REQUIRED", failure["code"])
				args["expected_version"] = current.Version
				restored := callRaceTool(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
				require.NotEqual(t, current.Version, restored["version"])
				f.assertStored(t, path, "original 漢🙂")
				if pluginName == "pageindex" {
					require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"), "restore must not bypass PageIndex and enqueue RAG")
				} else {
					require.Equal(t, jobs+1, f.count(t, "mcp_file_index_jobs"))
				}
				require.Equal(t, snapshots+1, f.count(t, "mcp_file_versions"))

				// The same numeric history row is invisible in another tenant's namespace.
				other := f.auth
				other.APIKeyHash = strings.Repeat("b", 64)
				otherCtx := context.WithValue(f.ctx, ctxkeys.AuthContext, &other)
				failure = callRaceToolError(t, otherCtx, history[tools.FileReadVersionToolName], tools.FileReadVersionToolName, args)
				require.Equal(t, "NOT_FOUND", failure["code"])
			})
		}
	}
}

// TestFileIOHistoryPageCursorKeepsExactIDs preserves adjacent IDs above JavaScript precision.
func TestFileIOHistoryPageCursorKeepsExactIDs(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			path := "/paged.txt"
			for i := range 5 {
				f.write(t, i%2, path, strconv.Itoa(i), files.WriteModeTruncate, 0)
			}
			versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", path)
			require.NoError(t, err)
			for i, version := range versions {
				_, err := f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_file_versions SET id = ?
					WHERE id = ? AND apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`),
					int64(9007199254740993+i), version.ID, f.auth.APIKeyHash, "race", path, "")
				require.NoError(t, err)
			}
			first, err := f.svc[0].ListVersionPage(f.ctx, f.auth, "race", path, 0, 2)
			require.NoError(t, err)
			require.True(t, first.HasMore)
			require.Len(t, first.Versions, 2)
			cursor, err := files.ParseHistoryID(first.NextCursor)
			require.NoError(t, err)
			require.Greater(t, cursor, uint64(1<<53))
			second, err := f.svc[1].ListVersionPage(f.ctx, f.auth, "race", path, cursor, 2)
			require.NoError(t, err)
			require.False(t, second.HasMore)
			require.Empty(t, second.NextCursor)
			require.Len(t, second.Versions, 2)
			seen := map[string]bool{}
			for _, row := range append(first.Versions, second.Versions...) {
				require.False(t, seen[row.ID])
				seen[row.ID] = true
			}
			other := f.auth
			other.APIKeyHash = strings.Repeat("c", 64)
			empty, err := f.svc[1].ListVersionPage(f.ctx, other, "race", path, 0, 50)
			require.NoError(t, err)
			require.NotNil(t, empty.Versions)
			require.Empty(t, empty.Versions)
		})
	}
}

// TestFileIOHistoryIDContract rejects lossy, non-canonical and token-shaped identifiers.
func TestFileIOHistoryIDContract(t *testing.T) {
	for _, raw := range []string{"", "0", "01", "+1", " 1", "1 ", "1e2", "1.5", "-1", "9223372036854775808", strings.Repeat("a", 32) + ":1"} {
		_, err := files.ParseHistoryID(raw)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"1", "9007199254740993", "9223372036854775807"} {
		id, err := files.ParseHistoryID(raw)
		require.NoError(t, err)
		require.Equal(t, raw, strconv.FormatUint(id, 10))
	}
}

// TestFileIOHistoryNewRowsDoNotShiftOlderPages exercises insertion between cursor requests.
func TestFileIOHistoryNewRowsDoNotShiftOlderPages(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newRaceFixture(t, backend, nil)
			path := "/cursor.txt"
			for i := range 5 {
				f.write(t, 0, path, strconv.Itoa(i), files.WriteModeTruncate, 0)
			}
			first, err := f.svc[0].ListVersionPage(f.ctx, f.auth, "race", path, 0, 2)
			require.NoError(t, err)
			cursor, err := files.ParseHistoryID(first.NextCursor)
			require.NoError(t, err)
			f.write(t, 1, path, "new snapshot after first page", files.WriteModeTruncate, 0)
			second, err := f.svc[1].ListVersionPage(f.ctx, f.auth, "race", path, cursor, 2)
			require.NoError(t, err)
			require.Len(t, second.Versions, 2)
			require.False(t, second.HasMore)
			seen := map[string]bool{}
			for _, row := range append(first.Versions, second.Versions...) {
				require.False(t, seen[row.ID], "duplicate history entry")
				seen[row.ID] = true
			}
			require.Len(t, seen, 4, "the four entries observed before insertion remain traversable")
			latest, err := f.svc[0].ListVersionPage(f.ctx, f.auth, "race", path, 0, 1)
			require.NoError(t, err)
			require.False(t, seen[latest.Versions[0].ID], "new row belongs to a refreshed first page")
		})
	}
}

// TestFileIOHistoryCreateOnlyRestoreDoesNotReplay recreates a deleted incarnation exactly once.
func TestFileIOHistoryCreateOnlyRestoreDoesNotReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newEntrypointFixture(t, backend, "rag")
			history := fixtureHistoryTools(t, f, "rag")
			path := "/recreate.txt"
			f.write(t, 0, path, "retained", files.WriteModeTruncate, 0)
			old := readRaceVersion(t, f.raceFixture, path)
			_, err := f.svc[1].Delete(f.ctx, f.auth, "race", path, false)
			require.NoError(t, err)
			versions, err := f.svc[0].ListVersionPage(f.ctx, f.auth, "race", path, 0, 1)
			require.NoError(t, err)
			args := map[string]any{"project": "race", "path": path, "history_id": versions.Versions[0].ID, "create_only": true}
			restored := callRaceTool(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
			version := restored["version"].(string)
			require.NotEqual(t, strings.Split(old.Version, ":")[0], strings.Split(version, ":")[0])
			jobs, rows := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
			failure := callRaceToolError(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
			require.Equal(t, "VERSION_CONFLICT", failure["code"])
			require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
			require.Equal(t, rows, f.count(t, "mcp_file_versions"))
			f.assertStored(t, path, "retained")
		})
	}
}

// TestFileIOHistoryArgumentsAndLegacyBytes rejects lossy IDs and never repairs binary data silently.
func TestFileIOHistoryArgumentsAndLegacyBytes(t *testing.T) {
	f := newEntrypointFixture(t, "sqlite", "rag")
	history := fixtureHistoryTools(t, f, "rag")
	path := "/old.txt"
	f.write(t, 0, path, "before", files.WriteModeTruncate, 0)
	f.write(t, 0, path, "current", files.WriteModeTruncate, 0)
	versions, err := f.svc[0].ListVersionPage(f.ctx, f.auth, "race", path, 0, 1)
	require.NoError(t, err)
	id := versions.Versions[0].ID
	for _, value := range []any{nil, float64(1), true, "01", "0", "9223372036854775808"} {
		failure := callRaceToolError(t, f.toolCtx, history[tools.FileReadVersionToolName], tools.FileReadVersionToolName,
			map[string]any{"project": "race", "path": path, "history_id": value})
		require.Equal(t, "INVALID_ARGUMENT", failure["code"])
	}
	_, err = f.db[0].ExecContext(f.ctx, f.query(`UPDATE mcp_file_versions SET content = ?, size = ?
		WHERE id = ? AND apikey_hash = ? AND project = ? AND path = ? AND system_owner = ?`),
		[]byte{0xff, 0xfe}, 2, id, f.auth.APIKeyHash, "race", path, "")
	require.NoError(t, err)
	args := map[string]any{"project": "race", "path": path, "history_id": id}
	read := callRaceTool(t, f.toolCtx, history[tools.FileReadVersionToolName], tools.FileReadVersionToolName, args)
	require.Equal(t, "base64", read["content_encoding"])
	require.Equal(t, "//4=", read["content"])
	base := readRaceVersion(t, f.raceFixture, path)
	args["expected_version"] = base.Version
	jobs, rows := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
	failure := callRaceToolError(t, f.toolCtx, history[tools.FileRestoreVersionToolName], tools.FileRestoreVersionToolName, args)
	require.Equal(t, "INVALID_CONTENT", failure["code"])
	require.Equal(t, base, readRaceVersion(t, f.raceFixture, path))
	require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
	require.Equal(t, rows, f.count(t, "mcp_file_versions"))
}
