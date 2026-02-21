package memory

import sdkmemory "github.com/Laisky/go-utils/v6/agents/memory"

// ResponseItem is a local alias of go-utils memory response item.
type ResponseItem = sdkmemory.ResponseItem

// ResponseContentPart is a local alias of go-utils memory content part.
type ResponseContentPart = sdkmemory.ResponseContentPart

// BeforeTurnRequest defines the MCP memory_before_turn request payload.
type BeforeTurnRequest struct {
	Project          string                   `json:"project"`
	SessionID        string                   `json:"session_id"`
	UserID           string                   `json:"user_id"`
	TurnID           string                   `json:"turn_id"`
	CurrentInput     []sdkmemory.ResponseItem `json:"current_input"`
	BaseInstructions string                   `json:"base_instructions"`
	MaxInputTok      int                      `json:"max_input_tok"`
}

// BeforeTurnResponse defines the MCP memory_before_turn response payload.
type BeforeTurnResponse struct {
	InputItems        []sdkmemory.ResponseItem `json:"input_items"`
	RecallFactIDs     []string                 `json:"recall_fact_ids"`
	ContextTokenCount int                      `json:"context_token_count"`
}

// AfterTurnRequest defines the MCP memory_after_turn request payload.
type AfterTurnRequest struct {
	Project     string                   `json:"project"`
	SessionID   string                   `json:"session_id"`
	UserID      string                   `json:"user_id"`
	TurnID      string                   `json:"turn_id"`
	InputItems  []sdkmemory.ResponseItem `json:"input_items"`
	OutputItems []sdkmemory.ResponseItem `json:"output_items"`
}

// SessionRequest defines shared request fields for session-scoped maintenance/introspection operations.
type SessionRequest struct {
	Project   string `json:"project"`
	SessionID string `json:"session_id"`
}

// ListDirWithAbstractRequest defines the MCP memory_list_dir_with_abstract request payload.
type ListDirWithAbstractRequest struct {
	Project   string `json:"project"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Depth     int    `json:"depth"`
	Limit     int    `json:"limit"`
}

// ListDirWithAbstractResponse defines the MCP memory_list_dir_with_abstract response payload.
type ListDirWithAbstractResponse struct {
	Summaries []sdkmemory.DirectorySummary `json:"summaries"`
}
