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

// TestGraphQLFileIOBoundary runs against the executable generated schema, which
// includes every subgraph in gqlgen.yml. FileIO currently lives at MCP and REST,
// not GraphQL. Adding a real GraphQL FileIO API must update this inventory AND
// add shared-version contract tests; do not silently expose raw Service writes.
func TestGraphQLFileIOBoundary(t *testing.T) {
	executable := NewExecutableSchema(Config{})
	schema := executable.Schema()
	require.NotNil(t, schema.Query)
	require.NotNil(t, schema.Mutation)
	require.NotNil(t, schema.Mutation.Fields.ForName("ExtractKeyInfo"))
	for _, name := range []string{"file_read", "file_write", "file_delete", "file_rename", "FileRead", "FileWrite", "FileDelete", "FileRename", "RestoreFileVersion"} {
		require.Nil(t, schema.Query.Fields.ForName(name), name)
		require.Nil(t, schema.Mutation.Fields.ForName(name), name)
	}

	// Validation must reject unknown operations before any resolver/database call.
	// Nil resolvers deliberately make an accidental execution fail the test.
	server := handler.NewDefaultServer(executable)
	for _, query := range []string{
		`query { file_read(project:"p",path:"/a") { content version } }`,
		`mutation { file_write(project:"p",path:"/a",content:"bad") { version } }`,
		`mutation { renamed: FileRename(project:"p",from_path:"/a",to_path:"/b") { moved_count } }`,
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
}
