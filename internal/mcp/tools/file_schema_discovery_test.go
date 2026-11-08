package tools

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// fileMutationDefinitions keeps write and restore on the same client contract.
func fileMutationDefinitions(t *testing.T) []mcp.Tool {
	t.Helper()
	writer, err := NewFileWriteTool(stubFileService{})
	require.NoError(t, err)
	restore := &FileHistoryTool{name: FileRestoreVersionToolName}
	return []mcp.Tool{writer.Definition(), restore.Definition()}
}

// listedFileSchema uses the actual tools/list JSON boundary, not InputSchema,
// which is intentionally empty for tools with a raw schema.
func listedFileSchema(t *testing.T, definition mcp.Tool) map[string]any {
	t.Helper()
	srv := server.NewMCPServer("fileio-schema-test", "1.0.0")
	srv.AddTool(definition, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("accepted"), nil
	})
	response := srv.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var envelope struct {
		Result struct {
			Tools []struct {
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.Len(t, envelope.Result.Tools, 1)
	return envelope.Result.Tools[0].InputSchema
}

// TestFileMutationSchemasExposeCompleteUnionArguments reproduces a client that
// renders oneOf alternatives as argument objects without merging parent fields.
// The native generator is external; this tests that compatibility boundary,
// not an invented implementation of that generator.
func TestFileMutationSchemasExposeCompleteUnionArguments(t *testing.T) {
	for _, definition := range fileMutationDefinitions(t) {
		t.Run(definition.Name, func(t *testing.T) {
			finder := &FindToolTool{logger: log.Logger}
			finder.SetTools([]mcp.Tool{definition})
			require.Contains(t, finder.searchBM25(definition.Name), definition.Name)
			found, err := finder.buildResponse([]string{definition.Name}, false)
			require.NoError(t, err)
			payload := behaviorJSONContent(t, found)
			discovered := payload["tools"].([]any)[0].(map[string]any)["inputSchema"].(map[string]any)
			listed := listedFileSchema(t, definition)
			require.Equal(t, listed, discovered, "tools/list and find_tool must publish the same contract")
			for _, schema := range []map[string]any{listed, discovered} {
				properties := schema["properties"].(map[string]any)
				require.NotContains(t, properties[createOnlyKey], "const", "branch construction must not mutate the root")
				alternatives := schema["oneOf"].([]any)
				require.Len(t, alternatives, 2)
				for index, alternative := range alternatives {
					branch := alternative.(map[string]any)
					require.Equal(t, "object", branch["type"], "union alternative %d must be independently usable", index)
					fields := branch["properties"].(map[string]any)
					require.Equal(t, index == 1, fields[createOnlyKey].(map[string]any)["const"])
					// A branch-oriented argument declaration must expose ALL common fields,
					// including optional mode/plugin/version, with their original constraints.
					require.Len(t, fields, len(properties))
					for name, property := range properties {
						require.Contains(t, fields, name, "argument %s disappeared from union alternative %d", name, index)
						if name == createOnlyKey {
							require.Equal(t, property.(map[string]any)["type"], fields[name].(map[string]any)["type"])
							require.Equal(t, property.(map[string]any)["description"], fields[name].(map[string]any)["description"])
						} else {
							require.Equal(t, property, fields[name])
						}
					}
					for _, required := range schema["required"].([]any) {
						require.Contains(t, branch["required"], required, "common argument must remain required")
					}
				}
			}
		})
	}
	// False-positive control: a normal flat reader schema already advertises its
	// input fields and does not acquire mutation conditions.
	reader, err := NewFileReadTool(stubFileService{})
	require.NoError(t, err)
	read := listedFileSchema(t, reader.Definition())
	require.NotContains(t, read, "oneOf")
	require.Contains(t, read["properties"], "project")
	require.Contains(t, read["properties"], "path")
	require.NotContains(t, read["required"], expectedVersionKey)
}

// schemaAcceptsFileArguments exercises the SDK's real JSON Schema validator;
// the handler must never run for rejected input. No production service is used.
func schemaAcceptsFileArguments(t *testing.T, schema map[string]any, args map[string]any) bool {
	t.Helper()
	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	srv := server.NewMCPServer("fileio-schema-test", "1.0.0", server.WithInputSchemaValidation())
	called := false
	srv.AddTool(mcp.NewToolWithRawSchema("mutation", "", encoded),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			called = true
			return mcp.NewToolResultText("accepted"), nil
		})
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "mutation", "arguments": args},
	})
	require.NoError(t, err)
	response, ok := srv.HandleMessage(t.Context(), request).(mcp.JSONRPCResponse)
	require.True(t, ok)
	result, ok := response.Result.(*mcp.CallToolResult)
	require.True(t, ok)
	require.Equal(t, called, !result.IsError, "validation failure must not enter the handler")
	return !result.IsError
}

// TestFileMutationSchemasKeepPreconditions validates both the full schema and
// standalone alternatives, so adding discoverable fields cannot weaken safety.
func TestFileMutationSchemasKeepPreconditions(t *testing.T) {
	version := "0123456789abcdef0123456789abcdef:7"
	cases := []struct {
		name      string
		condition map[string]any
		valid     bool
	}{
		{"version", map[string]any{expectedVersionKey: version}, true},
		{"version_false", map[string]any{expectedVersionKey: version, createOnlyKey: false}, true},
		{"create_only", map[string]any{createOnlyKey: true}, true},
		{"missing", map[string]any{}, false},
		{"false_without_version", map[string]any{createOnlyKey: false}, false},
		{"both", map[string]any{expectedVersionKey: version, createOnlyKey: true}, false},
		{"empty_version", map[string]any{expectedVersionKey: ""}, false},
		{"bad_version", map[string]any{expectedVersionKey: "7"}, false},
		{"null_version", map[string]any{expectedVersionKey: nil}, false},
		{"numeric_version", map[string]any{expectedVersionKey: 7}, false},
		{"null_create", map[string]any{expectedVersionKey: version, createOnlyKey: nil}, false},
		{"string_create", map[string]any{createOnlyKey: "true"}, false},
	}
	for _, definition := range fileMutationDefinitions(t) {
		t.Run(definition.Name, func(t *testing.T) {
			schema := listedFileSchema(t, definition)
			args := map[string]any{"project": "fixture", "path": "/synthetic.txt"}
			if definition.Name == "file_write" {
				args["content"] = "synthetic"
			} else {
				args["history_id"] = "17"
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					input := maps.Clone(args)
					maps.Copy(input, tc.condition)
					require.Equal(t, tc.valid, schemaAcceptsFileArguments(t, schema, input))
					accepted := 0
					for _, alternative := range schema["oneOf"].([]any) {
						if schemaAcceptsFileArguments(t, alternative.(map[string]any), input) {
							accepted++
						}
					}
					if tc.valid {
						require.Equal(t, 1, accepted)
					} else {
						require.Zero(t, accepted)
					}
				})
			}
			for _, key := range schema["required"].([]any) {
				t.Run("missing_"+key.(string), func(t *testing.T) {
					input := maps.Clone(args)
					input[expectedVersionKey] = version
					delete(input, key.(string))
					require.False(t, schemaAcceptsFileArguments(t, schema, input))
					for _, alternative := range schema["oneOf"].([]any) {
						require.False(t, schemaAcceptsFileArguments(t, alternative.(map[string]any), input))
					}
				})
			}
		})
	}
}
