package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSPAHandlerServesExtensionlessDiscoveryJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".well-known"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<html></html>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".well-known", "oauth-protected-resource"), []byte(`{"resource":"https://mcp.laisky.com"}`), 0o600))

	handler := &spaHandler{
		root:  root,
		index: []byte("<html></html>"),
		base:  "",
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	require.JSONEq(t, `{"resource":"https://mcp.laisky.com"}`, w.Body.String())
}

func TestShouldServeStaticJSON(t *testing.T) {
	t.Parallel()

	require.True(t, shouldServeStaticJSON(".well-known/api-catalog"))
	require.True(t, shouldServeStaticJSON(".well-known/oauth-authorization-server"))
	require.True(t, shouldServeStaticJSON(".well-known/oauth-protected-resource"))
	require.False(t, shouldServeStaticJSON("index.md"))
	require.False(t, shouldServeStaticJSON(".well-known/agent-card.json"))
}
