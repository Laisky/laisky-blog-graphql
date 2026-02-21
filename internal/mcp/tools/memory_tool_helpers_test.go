package tools

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// TestApplyMemoryDefaultsBeforeTurn verifies default injection for before_turn requests.
func TestApplyMemoryDefaultsBeforeTurn(t *testing.T) {
	request := mcpmemory.BeforeTurnRequest{}
	applyMemoryDefaultsBeforeTurn(&request)

	require.Equal(t, defaultMemoryProject, request.Project)
	require.Equal(t, defaultMemorySessionID, request.SessionID)
	require.Equal(t, defaultMaxInputTok, request.MaxInputTok)
	require.Regexp(t, `^turn-\d{13}-[0-9a-f]{6}$`, request.TurnID)
}

// TestApplyMemoryDefaultsBeforeTurnPreservesValues verifies user-provided values are respected.
func TestApplyMemoryDefaultsBeforeTurnPreservesValues(t *testing.T) {
	request := mcpmemory.BeforeTurnRequest{
		Project:     "project-alpha",
		SessionID:   "session-42",
		TurnID:      "turn-fixed",
		MaxInputTok: 4096,
	}
	applyMemoryDefaultsBeforeTurn(&request)

	require.Equal(t, "project-alpha", request.Project)
	require.Equal(t, "session-42", request.SessionID)
	require.Equal(t, "turn-fixed", request.TurnID)
	require.Equal(t, 4096, request.MaxInputTok)
}

// TestApplyMemoryDefaultsAfterTurn verifies default injection for after_turn requests.
func TestApplyMemoryDefaultsAfterTurn(t *testing.T) {
	request := mcpmemory.AfterTurnRequest{}
	applyMemoryDefaultsAfterTurn(&request)

	require.Equal(t, defaultMemoryProject, request.Project)
	require.Equal(t, defaultMemorySessionID, request.SessionID)
	require.Regexp(t, `^turn-\d{13}-[0-9a-f]{6}$`, request.TurnID)
}

// TestApplyMemoryDefaultsSession verifies default injection for session requests.
func TestApplyMemoryDefaultsSession(t *testing.T) {
	request := mcpmemory.SessionRequest{}
	applyMemoryDefaultsSession(&request)

	require.Equal(t, defaultMemoryProject, request.Project)
	require.Equal(t, defaultMemorySessionID, request.SessionID)
}

// TestApplyMemoryDefaultsListDir verifies default injection for list_dir requests.
func TestApplyMemoryDefaultsListDir(t *testing.T) {
	request := mcpmemory.ListDirWithAbstractRequest{}
	applyMemoryDefaultsListDir(&request)

	require.Equal(t, defaultMemoryProject, request.Project)
	require.Equal(t, defaultMemorySessionID, request.SessionID)
	require.Equal(t, defaultListDepth, request.Depth)
	require.Equal(t, defaultListLimit, request.Limit)
}

// TestGenerateMemoryTurnIDFormat verifies generated turn_id format is stable.
func TestGenerateMemoryTurnIDFormat(t *testing.T) {
	now := time.Date(2026, time.February, 21, 10, 11, 12, 0, time.UTC)
	turnID := generateMemoryTurnID(now)

	matcher := regexp.MustCompile(`^turn-` + regexp.QuoteMeta("1771668672000") + `-[0-9a-f]{6}$`)
	require.True(t, matcher.MatchString(turnID))
}
