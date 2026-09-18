package fileio

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	sdkmemory "github.com/Laisky/go-utils/v6/agents/memory"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// These defaults match internal/mcp/tools so both interfaces prepare identical
// requests for the same caller input.
const (
	defaultMemoryProject   = "default"
	defaultMemorySessionID = "default"
	defaultMaxInputTok     = 120000
	defaultListDepth       = 8
	defaultListLimit       = 200
	defaultHistoryLimit    = 50
	maxHistoryLimit        = 200
)

// stringOrDefault trims an optional argument and falls back when it is absent
// or blank, exactly as the MCP tools do.
func stringOrDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	if trimmed := strings.TrimSpace(*value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// turnID returns the caller's turn identifier or a fresh one. The generated
// form matches the MCP tools so audit rows are comparable across interfaces.
func turnID(value *string) string {
	if value != nil {
		if trimmed := strings.TrimSpace(*value); trimmed != "" {
			return trimmed
		}
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// A generated identifier only needs to be unique within the session;
		// fall back to the clock rather than failing the whole turn.
		return "turn-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	}
	return "turn-" + hex.EncodeToString(nonce[:])
}

// contentParts converts typed GraphQL content parts into SDK parts.
func contentParts(parts []*models.MemoryContentPartInput) []sdkmemory.ResponseContentPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]sdkmemory.ResponseContentPart, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		out = append(out, sdkmemory.ResponseContentPart{
			Type: part.Type, Text: valueOrEmpty(part.Text), ImageURL: valueOrEmpty(part.ImageURL),
			FileID: valueOrEmpty(part.FileID), Filename: valueOrEmpty(part.Filename),
		})
	}
	return out
}

// metadataMap converts the key/value list into the SDK metadata map. A repeated
// key keeps the last value, matching JSON object semantics.
func metadataMap(entries []*models.MemoryMetadataEntryInput) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		out[entry.Key] = entry.Value
	}
	return out
}

// requestItems converts typed GraphQL items into SDK conversation items.
func requestItems(items []*models.MemoryResponseItemInput) []sdkmemory.ResponseItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]sdkmemory.ResponseItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, sdkmemory.ResponseItem{
			Type: item.Type, Role: valueOrEmpty(item.Role), Content: contentParts(item.Content),
			CallID: valueOrEmpty(item.CallID), Output: valueOrEmpty(item.Output),
			Metadata: metadataMap(item.Metadata),
		})
	}
	return out
}

// responseItems converts SDK items into typed GraphQL items. Metadata is sorted
// by key so a response is deterministic for the same stored turn.
func responseItems(items []sdkmemory.ResponseItem) []*models.MemoryResponseItem {
	out := make([]*models.MemoryResponseItem, 0, len(items))
	for i := range items {
		item := items[i]
		content := make([]*models.MemoryContentPart, 0, len(item.Content))
		for j := range item.Content {
			part := item.Content[j]
			content = append(content, &models.MemoryContentPart{
				Type: part.Type, Text: optionalString(part.Text), ImageURL: optionalString(part.ImageURL),
				FileID: optionalString(part.FileID), Filename: optionalString(part.Filename),
			})
		}
		out = append(out, &models.MemoryResponseItem{
			Type: item.Type, Role: optionalString(item.Role), Content: content,
			CallID: optionalString(item.CallID), Output: optionalString(item.Output),
			Metadata: metadataEntries(item.Metadata),
		})
	}
	return out
}

// metadataEntries projects the metadata map into a stable key-ordered list.
func metadataEntries(metadata map[string]string) []*models.MemoryMetadataEntry {
	out := make([]*models.MemoryMetadataEntry, 0, len(metadata))
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		out = append(out, &models.MemoryMetadataEntry{Key: key, Value: metadata[key]})
	}
	return out
}

// sortStrings orders keys without pulling in a generics-heavy dependency.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// beforeTurnRequest builds the shared request, applying the same defaults and
// the same current_input_text compatibility conversion as the MCP tool.
func beforeTurnRequest(input models.MemoryBeforeTurnInput) (mcpmemory.BeforeTurnRequest, error) {
	request := mcpmemory.BeforeTurnRequest{
		Project:           stringOrDefault(input.Project, defaultMemoryProject),
		SessionID:         stringOrDefault(input.SessionID, defaultMemorySessionID),
		UserID:            valueOrEmpty(input.UserID),
		TurnID:            turnID(input.TurnID),
		ConversationItems: requestItems(input.ConversationItems),
		CurrentInput:      requestItems(input.CurrentInput),
		BaseInstructions:  valueOrEmpty(input.BaseInstructions),
		MaxInputTok:       defaultMaxInputTok,
	}
	if input.CurrentInputStart != nil {
		if *input.CurrentInputStart < 0 {
			return request, invalidArgument("current_input_start cannot be negative")
		}
		request.CurrentInputStart = *input.CurrentInputStart
	}
	if input.CurrentInputCount != nil {
		if *input.CurrentInputCount < 0 {
			return request, invalidArgument("current_input_count cannot be negative")
		}
		request.CurrentInputCount = *input.CurrentInputCount
	}
	if input.MaxInputTok != nil && *input.MaxInputTok > 0 {
		request.MaxInputTok = *input.MaxInputTok
	}
	if len(request.CurrentInput) == 0 && input.CurrentInputText != nil {
		text := strings.TrimSpace(*input.CurrentInputText)
		if text != "" {
			request.CurrentInput = []sdkmemory.ResponseItem{{
				Type: "message", Role: "user",
				Content: []sdkmemory.ResponseContentPart{{Type: "input_text", Text: text}},
			}}
		}
	}
	return request, nil
}

// afterTurnRequest builds the shared persistence request with MCP's defaults.
func afterTurnRequest(input models.MemoryAfterTurnInput) mcpmemory.AfterTurnRequest {
	request := mcpmemory.AfterTurnRequest{
		Project:           stringOrDefault(input.Project, defaultMemoryProject),
		SessionID:         stringOrDefault(input.SessionID, defaultMemorySessionID),
		UserID:            valueOrEmpty(input.UserID),
		TurnID:            turnID(input.TurnID),
		ConversationItems: requestItems(input.ConversationItems),
		InputItems:        requestItems(input.InputItems),
		OutputItems:       requestItems(input.OutputItems),
	}
	if input.CurrentInputStart != nil && *input.CurrentInputStart > 0 {
		request.CurrentInputStart = *input.CurrentInputStart
	}
	if input.CurrentInputCount != nil && *input.CurrentInputCount > 0 {
		request.CurrentInputCount = *input.CurrentInputCount
	}
	return request
}

// sessionRequest builds the shared session-scoped request with MCP's defaults.
func sessionRequest(project, sessionID *string) mcpmemory.SessionRequest {
	return mcpmemory.SessionRequest{
		Project:   stringOrDefault(project, defaultMemoryProject),
		SessionID: stringOrDefault(sessionID, defaultMemorySessionID),
	}
}

// mcpmemoryListRequest builds the shared directory-listing request.
func mcpmemoryListRequest(project, sessionID *string, path string, depth, limit int) mcpmemory.ListDirWithAbstractRequest {
	return mcpmemory.ListDirWithAbstractRequest{
		Project:   stringOrDefault(project, defaultMemoryProject),
		SessionID: stringOrDefault(sessionID, defaultMemorySessionID),
		Path:      path, Depth: depth, Limit: limit,
	}
}

// memoryAuditParameters records only routing identifiers. Conversation items
// are user content and must never reach an audit row.
func memoryAuditParameters(project, sessionID, turn string) map[string]any {
	parameters := map[string]any{fieldProject: project, "session_id": sessionID}
	if turn != "" {
		parameters["turn_id"] = turn
	}
	return parameters
}

// valueOrEmpty dereferences an optional string argument.
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// stringsOrEmpty normalizes a nil slice into an empty one so a non-null
// GraphQL list field never fails to serialize.
func stringsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
