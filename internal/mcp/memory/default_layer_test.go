package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestMemoryEngineKeepsSharedDefaultsOutOfProjectState runs a full memory turn
// and maintenance over a session whose paths also exist in the API key's
// @DEFAULT project. Memory state is project-local: the engine must create and
// rewrite the project's own files and never append to, compact, archive or
// delete a shared default, even though FileIO would show it through the project.
func TestMemoryEngineKeepsSharedDefaultsOutOfProjectState(t *testing.T) {
	service, db := newTestMemoryService(t)
	ctx := context.Background()
	auth := files.AuthContext{APIKey: "sk-test", APIKeyHash: "hash-test", UserIdentity: "user:test"}
	settings := files.LoadSettingsFromConfig()
	settings.Search.Enabled = false
	fileService, err := files.NewService(db, settings, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	seeded := map[string]string{}
	for _, path := range []string{
		"/memory/session-1/runtime/context/current.jsonl",
		"/memory/session-1/raw/shared-note.md",
		"/memory/session-1/notes.md",
	} {
		res, writeErr := fileService.Write(ctx, auth, files.DefaultProject, path, "shared default content\n", "utf-8", 0, files.WriteModeTruncate)
		require.NoError(t, writeErr)
		seeded[path] = res.Version
	}

	before, err := service.BeforeTurn(ctx, auth, BeforeTurnRequest{
		Project: "demo", SessionID: "session-1", UserID: "user-1", TurnID: "turn-1",
		CurrentInput: newTextItems("hello memory"), MaxInputTok: 120000,
	})
	require.NoError(t, err)
	require.NoError(t, service.AfterTurn(ctx, auth, AfterTurnRequest{
		Project: "demo", SessionID: "session-1", UserID: "user-1", TurnID: "turn-1",
		InputItems: before.InputItems, OutputItems: newTextItems("assistant response"),
	}))
	require.NoError(t, service.RunMaintenance(ctx, auth, SessionRequest{Project: "demo", SessionID: "session-1"}))

	for path, version := range seeded {
		stat, statErr := fileService.Stat(ctx, auth, files.DefaultProject, path)
		require.NoError(t, statErr)
		require.True(t, stat.Exists, "%s must survive memory maintenance", path)
		require.Equal(t, version, stat.Version, "%s must not be rewritten by project memory", path)
	}
	var projectFiles int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM mcp_files WHERE apikey_hash = ? AND project = ? AND path LIKE '/memory/session-1/%' AND deleted = FALSE AND system_owner = ''`,
		auth.APIKeyHash, "demo").Scan(&projectFiles))
	require.Positive(t, projectFiles, "the engine writes the project's own memory state")
}
