package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
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

// unionForbiddenArguments returns the arguments an alternative rejects through
// `not: {required: [...]}`; renderers that drop `not` must not see them declared.
func unionForbiddenArguments(branch map[string]any) map[string]bool {
	forbidden := make(map[string]bool)
	if not, ok := branch["not"].(map[string]any); ok {
		for _, name := range not["required"].([]any) {
			forbidden[name.(string)] = true
		}
	}
	return forbidden
}

// TestFileMutationSchemasExposeCompleteUnionArguments reproduces a client that
// renders oneOf alternatives as argument objects without merging parent fields.
// Codex CLI 0.169 code mode does exactly that (verified 2026-11-15, see
// docs/ref/20261108_fileio_schema_compatibility.md); this tests the boundary,
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
					forbidden := unionForbiddenArguments(branch)
					require.Equal(t, index == 1, forbidden[expectedVersionKey], "only the create alternative forbids a version")
					// A branch-oriented argument declaration must expose ALL common fields,
					// including optional mode/plugin, with their original constraints, and
					// must not advertise an argument the alternative itself rejects.
					require.Len(t, fields, len(properties)-len(forbidden))
					for name, property := range properties {
						if forbidden[name] {
							require.NotContains(t, fields, name, "union alternative %d advertises forbidden argument %s", index, name)
							continue
						}
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

// TestFileMutationAlternativesDeclareOnlyAcceptedArguments binds what each
// alternative declares to what the SDK validator accepts. Branch-oriented
// renderers (Codex CLI 0.169 code mode) turn an alternative's own properties
// and required list into the argument type and drop `not`: a declared but
// forbidden argument is then advertised as optional, and an accepted argument
// missing from the alternative disappears from the declaration.
func TestFileMutationAlternativesDeclareOnlyAcceptedArguments(t *testing.T) {
	samples := map[string]any{
		"project": "fixture", "path": "/synthetic.txt", contentKey: "synthetic", "content_encoding": "utf-8",
		"offset": 0, "mode": "APPEND", "plugin": "auto", "history_id": "17",
		expectedVersionKey: "0123456789abcdef0123456789abcdef:7",
	}
	for _, definition := range fileMutationDefinitions(t) {
		t.Run(definition.Name, func(t *testing.T) {
			schema := listedFileSchema(t, definition)
			for index, alternative := range schema["oneOf"].([]any) {
				branch := alternative.(map[string]any)
				declared := branch["properties"].(map[string]any)
				minimal := map[string]any{createOnlyKey: declared[createOnlyKey].(map[string]any)["const"]}
				for _, required := range branch["required"].([]any) {
					if name := required.(string); name != createOnlyKey {
						require.Contains(t, samples, name, "add a valid sample for required argument %s", name)
						minimal[name] = samples[name]
					}
				}
				require.True(t, schemaAcceptsFileArguments(t, branch, minimal), "alternative %d must accept its own minimal call", index)
				for name := range schema["properties"].(map[string]any) {
					t.Run(fmt.Sprintf("alternative_%d_%s", index, name), func(t *testing.T) {
						input := maps.Clone(minimal)
						if name != createOnlyKey {
							require.Contains(t, samples, name, "add a valid sample for argument %s", name)
							input[name] = samples[name]
						}
						_, advertised := declared[name]
						require.Equal(t, advertised, schemaAcceptsFileArguments(t, branch, input), "alternative declaration must match its validation")
						require.Equal(t, advertised, schemaAcceptsFileArguments(t, schema, input), "full schema must agree with the alternative")
					})
				}
			}
		})
	}
}

// factoredFileMutationSchema rebuilds the earlier representation of the same
// contract: common arguments only at the root and condition-only alternatives.
func factoredFileMutationSchema(schema map[string]any) map[string]any {
	common := make(map[string]bool)
	for _, name := range schema["required"].([]any) {
		common[name.(string)] = true
	}
	alternatives := make([]any, 0, 2)
	for _, alternative := range schema["oneOf"].([]any) {
		branch := alternative.(map[string]any)
		required := make([]any, 0, 1)
		for _, name := range branch["required"].([]any) {
			if !common[name.(string)] {
				required = append(required, name)
			}
		}
		createOnly := branch["properties"].(map[string]any)[createOnlyKey].(map[string]any)["const"]
		condition := map[string]any{"required": required, "properties": map[string]any{createOnlyKey: map[string]any{"const": createOnly}}}
		if not, ok := branch["not"]; ok {
			condition["not"] = not
		}
		alternatives = append(alternatives, condition)
	}
	factored := maps.Clone(schema)
	factored["oneOf"] = alternatives
	return factored
}

// TestFileMutationUnionsKeepLexicalDiscovery rules out a find_tool ranking
// regression from complete alternatives. rag.Tokenize returns unique tokens, so
// BM25 term frequency and document length depend only on the token set, which
// repeating root arguments inside alternatives must not change.
func TestFileMutationUnionsKeepLexicalDiscovery(t *testing.T) {
	reader, err := NewFileReadTool(stubFileService{})
	require.NoError(t, err)
	complete := append(fileMutationDefinitions(t), reader.Definition())
	factored := make([]mcp.Tool, 0, len(complete))
	for _, definition := range complete {
		schema := listedFileSchema(t, definition)
		if _, ok := schema["oneOf"]; !ok {
			factored = append(factored, definition)
			continue
		}
		encoded, err := json.Marshal(factoredFileMutationSchema(schema))
		require.NoError(t, err)
		older := mcp.NewToolWithRawSchema(definition.Name, definition.Description, encoded)
		require.ElementsMatch(t, rag.Tokenize(buildToolDocument(older)), rag.Tokenize(buildToolDocument(definition)), definition.Name)
		factored = append(factored, older)
	}
	before := &FindToolTool{logger: log.Logger}
	before.SetTools(factored)
	after := &FindToolTool{logger: log.Logger}
	after.SetTools(complete)
	for _, query := range []string{"write file content", "create a new file", "restore history version", "expected_version create_only", "read file"} {
		require.Equal(t, before.searchBM25(query), after.searchBM25(query), query)
	}
}
