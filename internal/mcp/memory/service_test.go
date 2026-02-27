package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

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
	err = db.QueryRowContext(context.Background(),
		"SELECT COUNT(1) FROM turn_guards WHERE api_key_hash = ? AND project = ? AND session_id = ? AND turn_id = ?",
		auth.APIKeyHash,
		"demo",
		"session-1",
		"turn-1",
	).Scan(&guardCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), guardCount)
}

// TestServiceAfterTurnPersistsOnlyDeltaInput verifies after_turn persistence drops recalled prefixes and memory reference blocks.
func TestServiceAfterTurnPersistsOnlyDeltaInput(t *testing.T) {
	service, _ := newTestMemoryService(t)
	auth := files.AuthContext{APIKey: "sk-test", APIKeyHash: "hash-test", UserIdentity: "user:test"}
	ctx := context.Background()

	firstBefore, err := service.BeforeTurn(ctx, auth, BeforeTurnRequest{
		Project:      "demo",
		SessionID:    "session-delta",
		UserID:       "user-1",
		TurnID:       "turn-1",
		CurrentInput: newTextItems("I prefer concise replies"),
		MaxInputTok:  120000,
	})
	require.NoError(t, err)
	require.NoError(t, service.AfterTurn(ctx, auth, AfterTurnRequest{
		Project:     "demo",
		SessionID:   "session-delta",
		UserID:      "user-1",
		TurnID:      "turn-1",
		InputItems:  firstBefore.InputItems,
		OutputItems: newAssistantTextItems("Understood."),
	}))

	secondBefore, err := service.BeforeTurn(ctx, auth, BeforeTurnRequest{
		Project:      "demo",
		SessionID:    "session-delta",
		UserID:       "user-1",
		TurnID:       "turn-2",
		CurrentInput: newTextItems("Please summarize this plan"),
		MaxInputTok:  120000,
	})
	require.NoError(t, err)
	require.NoError(t, service.AfterTurn(ctx, auth, AfterTurnRequest{
		Project:     "demo",
		SessionID:   "session-delta",
		UserID:      "user-1",
		TurnID:      "turn-2",
		InputItems:  secondBefore.InputItems,
		OutputItems: newAssistantTextItems("Summary ready."),
	}))

	runtimeContent, readErr := service.fileService.Read(ctx, auth, "demo", "/memory/session-delta/runtime/context/current.jsonl", 0, -1)
	require.NoError(t, readErr)

	events := parseRuntimeEventsJSONL(t, runtimeContent.Content)
	var turn2InputTexts []string
	for _, event := range events {
		if event.TurnID != "turn-2" || event.Type != "input_item" {
			continue
		}
		for _, part := range event.Item.Content {
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			turn2InputTexts = append(turn2InputTexts, part.Text)
		}
	}

	require.NotEmpty(t, turn2InputTexts)
	require.Equal(t, 1, len(turn2InputTexts))
	require.Contains(t, turn2InputTexts[0], "Please summarize this plan")
	require.NotContains(t, turn2InputTexts[0], "<memory_reference>")
	require.NotContains(t, turn2InputTexts[0], "I prefer concise replies")
}

// TestServiceBeforeTurnRequiresCurrentInput verifies BeforeTurn rejects empty current_input payloads.
func TestServiceBeforeTurnRequiresCurrentInput(t *testing.T) {
	service, _ := newTestMemoryService(t)
	auth := files.AuthContext{APIKey: "sk-test", APIKeyHash: "hash-test", UserIdentity: "user:test"}

	_, err := service.BeforeTurn(context.Background(), auth, BeforeTurnRequest{
		Project:      "demo",
		SessionID:    "session-empty-input",
		UserID:       "user-1",
		TurnID:       "turn-empty-input",
		CurrentInput: nil,
		MaxInputTok:  120000,
	})
	require.Error(t, err)
	asserted, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, ErrCodeInvalidArgument, asserted.Code)
	require.Equal(t, "current_input is required", asserted.Message)
}

// newTestMemoryService creates a memory service backed by sqlite and real FileIO service.
func newTestMemoryService(t *testing.T) (*Service, *sql.DB) {
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

// newAssistantTextItems builds a single assistant message item for SDK-compatible payloads.
func newAssistantTextItems(text string) []ResponseItem {
	return []ResponseItem{{
		Type: "message",
		Role: "assistant",
		Content: []ResponseContentPart{{
			Type: "output_text",
			Text: text,
		}},
	}}
}

// runtimeLogEvent is a local JSON shape used to decode memory runtime context events in tests.
type runtimeLogEvent struct {
	Type   string      `json:"type"`
	TurnID string      `json:"turn_id"`
	Item   runtimeItem `json:"item"`
}

// runtimeItem is a local JSON shape used to decode event item payloads in tests.
type runtimeItem struct {
	Content []runtimePart `json:"content"`
}

// runtimePart is a local JSON shape used to decode message content parts in tests.
type runtimePart struct {
	Text string `json:"text"`
}

// parseRuntimeEventsJSONL parses JSONL runtime context content into decoded event records.
func parseRuntimeEventsJSONL(t *testing.T, body string) []runtimeLogEvent {
	t.Helper()

	lines := strings.Split(body, "\n")
	events := make([]runtimeLogEvent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		event := runtimeLogEvent{}
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		events = append(events, event)
	}

	return events
}
