package tools

import (
	"encoding/json"

	errors "github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"
)

// toolInputSchemaJSON uses the same marshaler as tools/list. Reading InputSchema
// directly discards RawInputSchema and its oneOf constraints on file_write.
func toolInputSchemaJSON(definition mcp.Tool) (json.RawMessage, error) {
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, errors.Wrap(err, "marshal tool definition")
	}
	var wire struct {
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, errors.Wrap(err, "read serialized tool input schema")
	}
	if len(wire.InputSchema) == 0 || string(wire.InputSchema) == "null" {
		return nil, errors.New("tool definition has no input schema")
	}
	return wire.InputSchema, nil
}
