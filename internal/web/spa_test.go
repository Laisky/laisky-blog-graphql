package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
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
	require.Equal(t, "application/json; charset=utf-8", staticContentType(".well-known/webmcp"))
	require.Empty(t, staticContentType("index.md"))
	require.Empty(t, staticContentType(".well-known/agent-card.json"))
}

func TestSPAHandlerCachesFingerprintedBuildAssetsImmutably(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<html></html>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "assets", "page-Dxeb0V0_.js"), []byte("export {};"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "llms.txt"), []byte("# Laisky MCP"), 0o600))

	handler := &spaHandler{root: root, index: []byte("<html></html>"), logger: glog.Shared}
	serve := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	asset := serve("/assets/page-Dxeb0V0_.js")
	require.Equal(t, http.StatusOK, asset.Code)
	require.Equal(t, "public, max-age=31536000, immutable", asset.Header().Get("Cache-Control"))

	missing := serve("/assets/page-gone1234.js")
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Empty(t, missing.Header().Get("Cache-Control"), "a missing chunk must not be cached for a year")

	unhashed := serve("/llms.txt")
	require.Equal(t, http.StatusOK, unhashed.Code)
	require.Empty(t, unhashed.Header().Get("Cache-Control"), "files copied from public/ keep stable names and must revalidate")

	index := serve("/tools/web_search")
	require.Equal(t, http.StatusOK, index.Code)
	require.Equal(t, "no-store", index.Header().Get("Cache-Control"), "the entry document must always point at the current chunks")
}
