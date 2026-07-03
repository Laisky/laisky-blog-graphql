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
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.md"), []byte("# Laisky MCP\n\nAgent mode."), 0o600))
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

func TestSPAHandlerServesAgentModeMarkdown(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<html><body>interactive</body></html>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.md"), []byte("# Laisky MCP\n\nAgent mode."), 0o600))

	handler := &spaHandler{
		root:  root,
		index: []byte("<html><body>interactive</body></html>"),
	}

	req := httptest.NewRequest(http.MethodGet, "/?mode=agent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/markdown; charset=utf-8", w.Header().Get("Content-Type"))
	require.Contains(t, w.Header().Values("Vary"), "Accept")
	require.Contains(t, w.Body.String(), "# Laisky MCP")
}

func TestSPAHandlerServesAgentAPIProbeChallenge(t *testing.T) {
	t.Parallel()

	handler := &spaHandler{
		root:  t.TempDir(),
		index: []byte("<html></html>"),
	}

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, `Bearer resource_metadata="https://mcp.laisky.com/.well-known/oauth-protected-resource"`, w.Header().Get("WWW-Authenticate"))
	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	require.JSONEq(t, `{"error":"authentication_required","error_description":"Use a Laisky MCP API key in the Authorization header.","resource_metadata":"https://mcp.laisky.com/.well-known/oauth-protected-resource"}`, w.Body.String())
}

func TestStaticContentType(t *testing.T) {
	t.Parallel()

	require.Contains(t, staticContentType(".well-known/api-catalog"), "application/linkset+json")
	require.Equal(t, "application/json; charset=utf-8", staticContentType(".well-known/http-message-signatures-directory"))
	require.Equal(t, "application/json; charset=utf-8", staticContentType(".well-known/oauth-authorization-server"))
	require.Equal(t, "application/json; charset=utf-8", staticContentType(".well-known/oauth-protected-resource"))
	require.Empty(t, staticContentType("index.md"))
	require.Empty(t, staticContentType(".well-known/agent-card.json"))
}
