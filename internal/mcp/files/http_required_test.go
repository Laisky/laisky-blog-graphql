package files

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFileIOHTTPRequiresPreconditions verifies public HTTP is not a blind-write
// escape hatch around the mandatory MCP contract, for edits or history restore.
func TestFileIOHTTPRequiresPreconditions(t *testing.T) {
	svc, handler, auth := newHTTPTestEnv(t)
	ctx := context.Background()
	_, err := svc.Write(ctx, auth, "proj", "/a.txt", "A", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	_, err = svc.Write(ctx, auth, "proj", "/a.txt", "B", "utf-8", 0, WriteModeTruncate)
	require.NoError(t, err)
	before, err := svc.Read(ctx, auth, "proj", "/a.txt", 0, -1)
	require.NoError(t, err)
	history, err := svc.ListVersions(ctx, auth, "proj", "/a.txt")
	require.NoError(t, err)
	require.Len(t, history, 1)
	for _, request := range []struct{ method, url, body string }{
		{http.MethodPut, "/api/file", `{"project":"proj","path":"/a.txt","content":"unprotected"}`},
		{http.MethodPut, "/api/file", `{"project":"proj","path":"/new.txt","content":"unprotected"}`},
		{http.MethodPost, fmt.Sprintf("/api/versions/%d/restore", history[0].ID), `{"project":"proj","path":"/a.txt"}`},
	} {
		req := httptest.NewRequest(request.method, request.url, strings.NewReader(request.body))
		req.Header.Set("Authorization", httpAuthHeader())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		require.Equal(t, http.StatusPreconditionRequired, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		after, err := svc.Read(ctx, auth, "proj", "/a.txt", 0, -1)
		require.NoError(t, err)
		require.Equal(t, before, after)
		versions, err := svc.ListVersions(ctx, auth, "proj", "/a.txt")
		require.NoError(t, err)
		require.Len(t, versions, 1)
	}
	stat, err := svc.Stat(ctx, auth, "proj", "/new.txt")
	require.NoError(t, err)
	require.False(t, stat.Exists)
}
