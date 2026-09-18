package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/stretchr/testify/require"
)

// TestGraphQLFileIOInventory runs against the executable generated schema, which
// includes every subgraph in gqlgen.yml. FileIO and memory are now published on
// GraphQL as peer interfaces alongside MCP and the dedicated HTTP routes.
//
// This test is the schema-side half of the contract. It pins the published field
// inventory, the mandatory version preconditions and the exact-arithmetic types.
// The behavioral half — that a mutation without a precondition is rejected
// before it reaches storage — lives in fileio_graphql_contract_test.go.
func TestGraphQLFileIOInventory(t *testing.T) {
	executable := NewExecutableSchema(Config{})
	schema := executable.Schema()
	require.NotNil(t, schema.Query)
	require.NotNil(t, schema.Mutation)
	require.NotNil(t, schema.Mutation.Fields.ForName("ExtractKeyInfo"))

	for _, name := range []string{"FileStat", "FileRead", "FileList", "FileSearch",
		"FileListVersions", "FileReadVersion", "MemoryListDirWithAbstract"} {
		require.NotNil(t, schema.Query.Fields.ForName(name), name)
		require.Nil(t, schema.Mutation.Fields.ForName(name), name+" must not also be a mutation")
	}
	for _, name := range []string{"FileWrite", "FileDelete", "FileRename", "FileRestoreVersion",
		"MemoryBeforeTurn", "MemoryAfterTurn", "MemoryRunMaintenance"} {
		require.NotNil(t, schema.Mutation.Fields.ForName(name), name)
		require.Nil(t, schema.Query.Fields.ForName(name), name+" must not also be a query")
	}

	// The snake_case MCP tool names stay MCP-only identifiers. Publishing them
	// as GraphQL fields would create a second spelling of the same operation.
	for _, name := range []string{"file_read", "file_write", "file_delete", "file_rename",
		"file_stat", "file_list", "file_search", "memory_before_turn"} {
		require.Nil(t, schema.Query.Fields.ForName(name), name)
		require.Nil(t, schema.Mutation.Fields.ForName(name), name)
	}

	t.Run("delete and rename require a live version in the schema itself", func(t *testing.T) {
		for _, field := range []string{"FileDelete", "FileRename"} {
			argument := schema.Mutation.Fields.ForName(field).Arguments.ForName("expected_version")
			require.NotNil(t, argument, field)
			require.Equal(t, "String!", argument.Type.String(), field+" must not accept a null version")
		}
	})

	t.Run("write and restore offer both alternatives, neither optional at runtime", func(t *testing.T) {
		for _, field := range []string{"FileWrite", "FileRestoreVersion"} {
			definition := schema.Mutation.Fields.ForName(field)
			require.Equal(t, "String", definition.Arguments.ForName("expected_version").Type.String(), field)
			require.Equal(t, "Boolean", definition.Arguments.ForName("create_only").Type.String(), field)
		}
	})

	t.Run("byte counts and offsets use the exact 64-bit scalar", func(t *testing.T) {
		require.Equal(t, "BigInt", schema.Types["FileIOStatResult"].Fields.ForName("size").Type.Name())
		require.Equal(t, "BigInt", schema.Types["FileIOWriteResult"].Fields.ForName("bytes_written").Type.Name())
		require.Equal(t, "BigInt", schema.Types["FileIOHistoryEntry"].Fields.ForName("size").Type.Name())
		require.Equal(t, "BigInt", schema.Mutation.Fields.ForName("FileWrite").Arguments.ForName("offset").Type.Name())
		// History identifiers stay exact decimal strings, never numbers.
		require.Equal(t, "String!", schema.Types["FileIOHistoryEntry"].Fields.ForName("id").Type.String())
		require.Equal(t, "String!",
			schema.Query.Fields.ForName("FileReadVersion").Arguments.ForName("history_id").Type.String())
	})

	// Validation still rejects unknown operations before any resolver or
	// database call. Nil resolvers make an accidental execution fail the test.
	server := handler.NewDefaultServer(executable)
	for _, query := range []string{
		`query { file_read(project:"p",path:"/a") { content version } }`,
		`mutation { file_write(project:"p",path:"/a",content:"bad") { version } }`,
		`mutation { renamed: FileRenamed(project:"p",from_path:"/a",to_path:"/b") { moved_count } }`,
		`mutation { ...Unsafe } fragment Unsafe on Mutation { file_delete(project:"p",path:"/a") { deleted_count } }`,
	} {
		t.Run(query, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"query": query})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			var response struct {
				Data   json.RawMessage `json:"data"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.NotEmpty(t, response.Errors)
			require.Contains(t, response.Errors[0].Message, "Cannot query field")
			require.True(t, len(response.Data) == 0 || string(response.Data) == "null")
		})
	}

	// A mutation that omits both preconditions must fail GraphQL validation only
	// when the schema marks the argument non-null; FileWrite deliberately does
	// not, so it must reach the shared runtime gate instead of being accepted.
	t.Run("delete without a version fails validation before any resolver", func(t *testing.T) {
		body, err := json.Marshal(map[string]string{
			"query": `mutation { FileDelete(project:"p",path:"/a") { deleted_count } }`,
		})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		require.Contains(t, recorder.Body.String(), "expected_version")
	})
}
