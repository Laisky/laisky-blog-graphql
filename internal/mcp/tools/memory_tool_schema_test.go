package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
)

// schemaTestMemoryService is a no-op memory service used for schema definition tests.
type schemaTestMemoryService struct{}

// BeforeTurn returns an empty response for tests.
// It accepts the call context, auth context, and before-turn request, and returns a deterministic empty payload.
func (schemaTestMemoryService) BeforeTurn(context.Context, files.AuthContext, mcpmemory.BeforeTurnRequest) (mcpmemory.BeforeTurnResponse, error) {
	return mcpmemory.BeforeTurnResponse{}, nil
}

// AfterTurn accepts after-turn requests in tests.
// It accepts the call context, auth context, and after-turn request, and returns nil.
func (schemaTestMemoryService) AfterTurn(context.Context, files.AuthContext, mcpmemory.AfterTurnRequest) error {
	return nil
}

// RunMaintenance accepts maintenance requests in tests.
// It accepts the call context, auth context, and session request, and returns nil.
func (schemaTestMemoryService) RunMaintenance(context.Context, files.AuthContext, mcpmemory.SessionRequest) error {
	return nil
}

// ListDirWithAbstract returns an empty list in tests.
// It accepts the call context, auth context, and list request, and returns an empty response.
func (schemaTestMemoryService) ListDirWithAbstract(context.Context, files.AuthContext, mcpmemory.ListDirWithAbstractRequest) (mcpmemory.ListDirWithAbstractResponse, error) {
	return mcpmemory.ListDirWithAbstractResponse{}, nil
}

// TestMemoryBeforeTurnDefinitionCurrentInputIncludesItems verifies current_input array schema has an explicit items schema.
func TestMemoryBeforeTurnDefinitionCurrentInputIncludesItems(t *testing.T) {
	tool, err := NewMemoryBeforeTurnTool(schemaTestMemoryService{})
	require.NoError(t, err)

	definition := tool.Definition()
	require.Contains(t, definition.InputSchema.Required, "current_input")
	property, ok := definition.InputSchema.Properties["current_input"]
	require.True(t, ok)

	schema, ok := property.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "array", schema["type"])

	_, hasItems := schema["items"]
	require.True(t, hasItems)
}

// TestMemoryAfterTurnDefinitionArraysIncludeItems verifies input/output array schemas have explicit items schemas.
func TestMemoryAfterTurnDefinitionArraysIncludeItems(t *testing.T) {
	tool, err := NewMemoryAfterTurnTool(schemaTestMemoryService{})
	require.NoError(t, err)

	definition := tool.Definition()
	for _, propertyName := range []string{"input_items", "output_items"} {
		property, ok := definition.InputSchema.Properties[propertyName]
		require.True(t, ok)

		schema, ok := property.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "array", schema["type"])

		_, hasItems := schema["items"]
		require.True(t, hasItems)
	}
}

// TestMemoryBeforeTurnDefinitionMarshaledSchemaKeepsItems verifies current_input keeps items after JSON marshalling.
func TestMemoryBeforeTurnDefinitionMarshaledSchemaKeepsItems(t *testing.T) {
	tool, err := NewMemoryBeforeTurnTool(schemaTestMemoryService{})
	require.NoError(t, err)

	data, err := json.Marshal(tool.Definition())
	require.NoError(t, err)

	decoded := map[string]any{}
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	inputSchema, ok := decoded["inputSchema"].(map[string]any)
	require.True(t, ok)
	properties, ok := inputSchema["properties"].(map[string]any)
	require.True(t, ok)

	property, ok := properties["current_input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "array", property["type"])
	_, hasItems := property["items"]
	require.True(t, hasItems)
}

// TestMemoryAfterTurnDefinitionMarshaledSchemaKeepsItems verifies input/output arrays keep items after JSON marshalling.
func TestMemoryAfterTurnDefinitionMarshaledSchemaKeepsItems(t *testing.T) {
	tool, err := NewMemoryAfterTurnTool(schemaTestMemoryService{})
	require.NoError(t, err)

	data, err := json.Marshal(tool.Definition())
	require.NoError(t, err)

	decoded := map[string]any{}
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	inputSchema, ok := decoded["inputSchema"].(map[string]any)
	require.True(t, ok)
	properties, ok := inputSchema["properties"].(map[string]any)
	require.True(t, ok)

	for _, propertyName := range []string{"input_items", "output_items"} {
		property, ok := properties[propertyName].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "array", property["type"])
		_, hasItems := property["items"]
		require.True(t, hasItems)
	}
}
