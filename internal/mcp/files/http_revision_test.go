package files

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFileIOHTTPPreconditions exercises existing authenticated HTTP endpoints.
func TestFileIOHTTPPreconditions(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)
	invoke := func(url, body string, headers map[string]string) *httptest.ResponseRecorder {
		method := http.MethodPut
		if strings.HasSuffix(url, "/restore") {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", httpAuthHeader())
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	body := `{"project":"proj","path":"/a.txt","content":"A"}`
	first := invoke("/api/file", body, map[string]string{"If-None-Match": "*"})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var response map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &response))
	require.Equal(t, `"`+response["version"].(string)+`"`, first.Header().Get("ETag"))
	duplicate := invoke("/api/file", body, map[string]string{"If-None-Match": "*"})
	require.Equal(t, http.StatusPreconditionFailed, duplicate.Code)
	next := invoke("/api/file", strings.Replace(body, `"A"`, `"B"`, 1), map[string]string{"If-Match": first.Header().Get("ETag")})
	require.Equal(t, http.StatusOK, next.Code, next.Body.String())
	stale := invoke("/api/file", body, map[string]string{"If-Match": first.Header().Get("ETag")})
	require.Equal(t, http.StatusPreconditionFailed, stale.Code)
	for _, invalid := range []string{"", "*", `W/` + next.Header().Get("ETag"), next.Header().Get("ETag") + "," + first.Header().Get("ETag"), `"broken"`} {
		bad := invoke("/api/file", body, map[string]string{"If-Match": invalid})
		require.Equal(t, http.StatusBadRequest, bad.Code, bad.Body.String())
	}
	bad := invoke("/api/file", body, map[string]string{"If-Match": next.Header().Get("ETag"), "If-None-Match": "*"})
	require.Equal(t, http.StatusBadRequest, bad.Code)
	versions, err := svc.ListVersions(context.Background(), auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	url := fmt.Sprintf("/api/versions/%d/restore", versions[0].ID)
	restoreBody := `{"project":"proj","path":"/a.txt"}`
	stale = invoke(url, restoreBody, map[string]string{"If-Match": first.Header().Get("ETag")})
	require.Equal(t, http.StatusPreconditionFailed, stale.Code)
	restored := invoke(url, restoreBody, map[string]string{"If-Match": next.Header().Get("ETag")})
	require.Equal(t, http.StatusOK, restored.Code, restored.Body.String())
	require.NotEqual(t, next.Header().Get("ETag"), restored.Header().Get("ETag"))
	final, err := svc.Read(context.Background(), auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	require.Equal(t, "A", final.Content)
	require.Equal(t, `"`+final.Version+`"`, restored.Header().Get("ETag"))
}

// TestFileIORevisionMigrationBackfill verifies existing rows get stable identities
// without rewriting content, and repeated constructors do not reset revisions.
func TestFileIORevisionMigrationBackfill(t *testing.T) {
	db := newTestDB(t)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	for _, stmt := range migrationTableStatements(false) {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO mcp_files(apikey_hash,project,path,content,size,created_at,updated_at,deleted)
  VALUES('legacy','proj','/a',?,1,'2026-09-16 00:00:00','2026-09-16 00:00:00',FALSE)`, []byte("A"))
	require.NoError(t, err)
	require.NoError(t, RunMigrations(ctx, db, nil))
	read := func() (string, int64) {
		var incarnation string
		var revision int64
		require.NoError(t, db.QueryRowContext(ctx, `SELECT incarnation_id,revision FROM mcp_files WHERE apikey_hash='legacy' AND system_owner=''`).Scan(&incarnation, &revision))
		return incarnation, revision
	}
	identity, revision := read()
	require.Len(t, identity, 32)
	require.Equal(t, int64(1), revision)
	_, err = db.ExecContext(ctx, `UPDATE mcp_files SET content=content WHERE apikey_hash='legacy' AND system_owner=''`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(ctx, db, nil))
	again, revision := read()
	require.Equal(t, identity, again)
	require.Equal(t, int64(2), revision)
}
