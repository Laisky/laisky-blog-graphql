package tools

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestMemoryBeforeTurnHandleRequiresCurrentInput verifies tool-level responses return INVALID_ARGUMENT when current_input is missing.
func TestMemoryBeforeTurnHandleRequiresCurrentInput(t *testing.T) {
	memoryService := newTestToolMemoryService(t)
	tool, err := NewMemoryBeforeTurnTool(memoryService)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{
		APIKey:       "sk-test",
		APIKeyHash:   "hash-test",
		UserIdentity: "user:test",
	})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"project":           "bot",
		"session_id":        "task-2026-02-27-0012",
		"base_instructions": "Run a minimal memory_before_turn retrieval test and return full tool output for logging.",
		"max_input_tok":     120000,
	}}}

	result, handleErr := tool.Handle(ctx, req)
	require.NoError(t, handleErr)
	require.NotNil(t, result)
	require.True(t, result.IsError)

	payload := decodeToolPayload(t, result)
	require.Equal(t, string(mcpmemory.ErrCodeInvalidArgument), payload["code"])
	require.Equal(t, "current_input is required", payload["message"])
	require.Equal(t, false, payload["retryable"])
}

// newTestToolMemoryService creates a real memory service for tool-level regression tests.
func newTestToolMemoryService(t *testing.T) *mcpmemory.Service {
	t.Helper()

	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UTC().UnixNano())
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	fileSettings := files.LoadSettingsFromConfig()
	fileSettings.Search.Enabled = false
	fileSettings.MaxProjectBytes = 2_000_000
	fileSettings.MaxFileBytes = 1_000_000
	fileSettings.MaxPayloadBytes = 1_000_000

	fileService, err := files.NewService(db, fileSettings, nil, nil, nil, nil, log.Logger.Named("memory_before_turn_tool_test_files"), nil, nil)
	require.NoError(t, err)

	memoryService, err := mcpmemory.NewService(db, fileService, mcpmemory.LoadSettingsFromConfig(), log.Logger.Named("memory_before_turn_tool_test"), nil)
	require.NoError(t, err)

	return memoryService
}
