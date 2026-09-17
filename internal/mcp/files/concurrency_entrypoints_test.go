package files_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	pageindex "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/pageindex"
	ragplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugins/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
)

type entrypointFixture struct {
	*raceFixture
	writer  *tools.FileWriteTool
	reader  *tools.FileReadTool
	handler http.Handler
	toolCtx context.Context
}

func newEntrypointFixture(t *testing.T, backend, pluginName string) *entrypointFixture {
	t.Helper()
	f := newRaceFixture(t, backend, nil)
	auth, err := mcpauth.DeriveFromAPIKey("sk-fileio-cross-entrypoint-fixture")
	require.NoError(t, err)
	f.auth = files.AuthContext{APIKey: auth.APIKey, APIKeyHash: auth.APIKeyHash, UserID: auth.UserID, UserIdentity: auth.UserIdentity}
	managers := make([]*mcpplugin.Manager, 2)
	for i, service := range f.svc {
		var plugin mcpplugin.Plugin
		if pluginName == "pageindex" {
			system, err := service.SystemNamespace("pageindex")
			require.NoError(t, err)
			plugin, err = pageindex.New(pageindex.PluginDeps{UserFS: service, SystemFS: system})
			require.NoError(t, err)
		} else {
			plugin, err = ragplugin.New(service)
			require.NoError(t, err)
		}
		managers[i], err = mcpplugin.NewManager(pluginName, plugin)
		require.NoError(t, err)
	}
	writer, err := tools.NewFileWriteTool(managers[0])
	require.NoError(t, err)
	reader, err := tools.NewFileReadTool(managers[0])
	require.NoError(t, err)
	handler := files.NewHTTPHandler(f.svc[1], nil, files.WithHTTPFileWriterResolver(
		func(ctx context.Context, auth files.AuthContext, project string) (files.FileHTTPWriter, error) {
			return tools.ResolveVersionedFileService(ctx, managers[1], auth, project)
		},
	))
	return &entrypointFixture{raceFixture: f, writer: writer, reader: reader, handler: handler,
		toolCtx: context.WithValue(f.ctx, ctxkeys.AuthContext, &f.auth)}
}

func (f *entrypointFixture) request(method, endpoint, body, header, version string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, endpoint, strings.NewReader(body)).WithContext(f.ctx)
	request.Header.Set("Authorization", "Bearer "+f.auth.APIKey)
	request.Header.Set("Content-Type", "application/json")
	if header != "" {
		request.Header.Set(header, version)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func requireEntrypointWrite(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NoError(t, files.ValidateFileVersion(body.Version))
	require.Equal(t, `"`+body.Version+`"`, response.Header().Get("ETag"))
	return body.Version
}

// TestFileIOCrossEntrypointConditions exercises actual MCP handlers and the HTTP
// handler over independent service/database pools. Switching transport cannot
// bypass the original read token, nor can HTTP silently choose another plugin.
func TestFileIOCrossEntrypointConditions(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, pluginName := range []string{"rag", "pageindex"} {
			t.Run(backend+"/"+pluginName, func(t *testing.T) {
				f := newEntrypointFixture(t, backend, pluginName)
				path := "/shared.txt" // Deterministic PageIndex summary; no provider calls.
				args := map[string]any{"project": "race", "path": path, "mode": "TRUNCATE", "content": "A", "create_only": true}
				created := callRaceTool(t, f.toolCtx, f.writer, "file_write", args)
				v1 := created["version"].(string)
				response := f.request(http.MethodPut, "/api/file", `{"project":"race","path":"/shared.txt","content":"B漢"}`, "If-Match", `"`+v1+`"`)
				v2 := requireEntrypointWrite(t, response)
				require.NotEqual(t, v1, v2)
				before := readRaceVersion(t, f.raceFixture, path)
				jobs, history := f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
				delete(args, "create_only")
				args["expected_version"], args["content"] = v1, "obsolete MCP edit"
				require.Equal(t, "VERSION_CONFLICT", callRaceToolError(t, f.toolCtx, f.writer, "file_write", args)["code"])
				require.Equal(t, before, readRaceVersion(t, f.raceFixture, path))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				require.Equal(t, history, f.count(t, "mcp_file_versions"))

				args["expected_version"], args["content"] = v2, "C"
				updated := callRaceTool(t, f.toolCtx, f.writer, "file_write", args)
				v3 := updated["version"].(string)
				before = readRaceVersion(t, f.raceFixture, path)
				jobs, history = f.count(t, "mcp_file_index_jobs"), f.count(t, "mcp_file_versions")
				stale := f.request(http.MethodPut, "/api/file", `{"project":"race","path":"/shared.txt","content":"obsolete HTTP edit"}`, "If-Match", `"`+v2+`"`)
				require.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())
				missing := f.request(http.MethodPut, "/api/file", `{"project":"race","path":"/shared.txt","content":"unguarded"}`, "", "")
				require.Equal(t, http.StatusPreconditionRequired, missing.Code)
				require.Equal(t, before, readRaceVersion(t, f.raceFixture, path))
				require.Equal(t, jobs, f.count(t, "mcp_file_index_jobs"))
				require.Equal(t, history, f.count(t, "mcp_file_versions"))

				versions, err := f.svc[0].ListVersions(f.ctx, f.auth, "race", path)
				require.NoError(t, err)
				require.Len(t, versions, 2)
				endpoint := fmt.Sprintf("/api/versions/%d/restore", versions[1].ID)
				body := `{"project":"race","path":"/shared.txt"}`
				stale = f.request(http.MethodPost, endpoint, body, "If-Match", `"`+v2+`"`)
				require.Equal(t, http.StatusPreconditionFailed, stale.Code)
				require.Equal(t, before, readRaceVersion(t, f.raceFixture, path))
				response = f.request(http.MethodPost, endpoint, body, "If-Match", `"`+v3+`"`)
				v4 := requireEntrypointWrite(t, response)
				wire := callRaceTool(t, f.toolCtx, f.reader, "file_read", map[string]any{"project": "race", "path": path, "expected_version": v4})
				require.Equal(t, "A", wire["content"])
				require.NotEqual(t, v3, v4)
				f.assertStored(t, path, "A")

				if pluginName == "pageindex" {
					var skip bool
					var sourceHash, summaryHash string
					err := f.db[1].QueryRowContext(f.ctx, f.query(`SELECT skip_rag_index,content_hash,summary_content_hash FROM mcp_files
						WHERE apikey_hash = ? AND project = ? AND path = ? AND system_owner = ? AND deleted = FALSE`),
						f.auth.APIKeyHash, "race", path, "").Scan(&skip, &sourceHash, &summaryHash)
					require.NoError(t, err)
					require.True(t, skip, "HTTP save/restore must use the selected PageIndex writer")
					require.Equal(t, files.HashFileContent([]byte("A")), sourceHash)
					require.Equal(t, sourceHash, summaryHash, "routed restore must publish the restored generation")
					require.Zero(t, f.count(t, "mcp_file_index_jobs"))
				}
			})
		}
	}
}

// TestFileIOPostgresMCPAndHTTPContendOnOneVersion checks actual simultaneous
// transport histories rather than just sequential stale requests.
func TestFileIOPostgresMCPAndHTTPContendOnOneVersion(t *testing.T) {
	f := newEntrypointFixture(t, "postgres", "rag")
	seed := callRaceTool(t, f.toolCtx, f.writer, "file_write", map[string]any{
		"project": "race", "path": "/shared.txt", "content": "base", "mode": "TRUNCATE", "create_only": true,
	})
	version := seed["version"].(string)
	var mcpResult *mcp.CallToolResult
	var httpResult *httptest.ResponseRecorder
	errs := parallelRaceCalls(t, f.ctx, 2, func(i int) error {
		if i == 0 {
			var err error
			mcpResult, err = f.writer.Handle(f.toolCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
				"project": "race", "path": "/shared.txt", "content": "MCP漢", "mode": "TRUNCATE", "expected_version": version,
			}}})
			return err
		}
		httpResult = f.request(http.MethodPut, "/api/file", `{"project":"race","path":"/shared.txt","content":"HTTP漢"}`, "If-Match", `"`+version+`"`)
		return nil
	})
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.NotNil(t, mcpResult)
	require.NotNil(t, httpResult)
	if mcpResult.IsError {
		wire, err := json.Marshal(mcpResult)
		require.NoError(t, err)
		require.Contains(t, string(wire), "VERSION_CONFLICT")
		requireEntrypointWrite(t, httpResult)
		f.assertStored(t, "/shared.txt", "HTTP漢")
	} else {
		require.Equal(t, http.StatusPreconditionFailed, httpResult.Code)
		f.assertStored(t, "/shared.txt", "MCP漢")
	}
	require.Equal(t, 1, f.count(t, "mcp_file_versions"))
	require.Equal(t, 2, f.count(t, "mcp_file_index_jobs"))
}

func TestFileIOHTTPResolverFailureNeverFallsBackToStorage(t *testing.T) {
	f := newEntrypointFixture(t, "sqlite", "rag")
	f.handler = files.NewHTTPHandler(f.svc[1], nil, files.WithHTTPFileWriterResolver(nil))
	response := f.request(http.MethodPut, "/api/file", `{"project":"race","path":"/must-not-exist","content":"bad"}`, "If-None-Match", "*")
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Zero(t, f.count(t, "mcp_files"))
	require.Zero(t, f.count(t, "mcp_file_versions"))
	require.Zero(t, f.count(t, "mcp_file_index_jobs"))
}
