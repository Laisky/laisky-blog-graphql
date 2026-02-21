package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestServiceBeforeAfterTurnFlow verifies memory lifecycle methods against real FileIO-backed storage.
func TestServiceBeforeAfterTurnFlow(t *testing.T) {
	service, db := newTestMemoryService(t)
	auth := files.AuthContext{APIKey: "sk-test", APIKeyHash: "hash-test", UserIdentity: "user:test"}

	beforeOut, err := service.BeforeTurn(context.Background(), auth, BeforeTurnRequest{
		Project:      "demo",
		SessionID:    "session-1",
		UserID:       "user-1",
		TurnID:       "turn-1",
		CurrentInput: newTextItems("hello memory"),
		MaxInputTok:  120000,
	})
	require.NoError(t, err)
	require.NotEmpty(t, beforeOut.InputItems)

	err = service.AfterTurn(context.Background(), auth, AfterTurnRequest{
		Project:     "demo",
		SessionID:   "session-1",
		UserID:      "user-1",
		TurnID:      "turn-1",
		InputItems:  beforeOut.InputItems,
		OutputItems: newTextItems("assistant response"),
	})
	require.NoError(t, err)

	err = service.AfterTurn(context.Background(), auth, AfterTurnRequest{
		Project:     "demo",
		SessionID:   "session-1",
		UserID:      "user-1",
		TurnID:      "turn-1",
		InputItems:  beforeOut.InputItems,
		OutputItems: newTextItems("assistant response"),
	})
	require.NoError(t, err)

	err = service.RunMaintenance(context.Background(), auth, SessionRequest{Project: "demo", SessionID: "session-1"})
	require.NoError(t, err)

	listOut, err := service.ListDirWithAbstract(context.Background(), auth, ListDirWithAbstractRequest{
		Project:   "demo",
		SessionID: "session-1",
		Path:      "",
		Depth:     8,
		Limit:     200,
	})
	require.NoError(t, err)
	require.NotNil(t, listOut.Summaries)

	var guardCount int64
	err = db.Model(&TurnGuard{}).
		Where("api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?", auth.APIKeyHash, "demo", "session-1", "turn-1").
		Count(&guardCount).Error
	require.NoError(t, err)
	require.Equal(t, int64(1), guardCount)
}

// newTestMemoryService creates a memory service backed by sqlite and real FileIO service.
func newTestMemoryService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()

	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UTC().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	fileSettings := files.LoadSettingsFromConfig()
	fileSettings.Search.Enabled = false
	fileSettings.MaxProjectBytes = 2_000_000
	fileSettings.MaxFileBytes = 1_000_000
	fileSettings.MaxPayloadBytes = 1_000_000

	fileService, err := files.NewService(db, fileSettings, nil, nil, nil, nil, log.Logger.Named("memory_service_test_files"), nil, nil)
	require.NoError(t, err)

	memoryService, err := NewService(db, fileService, LoadSettingsFromConfig(), log.Logger.Named("memory_service_test"), nil)
	require.NoError(t, err)

	return memoryService, db
}

// newTextItems builds a single message item for SDK-compatible payloads.
func newTextItems(text string) []ResponseItem {
	return []ResponseItem{{
		Type: "message",
		Role: "user",
		Content: []ResponseContentPart{{
			Type: "input_text",
			Text: text,
		}},
	}}
}
