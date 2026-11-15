package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/gin-gonic/gin"
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

// TestAgentAPIProbesAdvertiseTheConfiguredMetadataURL covers review finding
// C for the gin and SPA probes: both point to the configured origin's RFC 9728
// document, and the shipped document is checked against that origin.
func TestAgentAPIProbesAdvertiseTheConfiguredMetadataURL(t *testing.T) {
	const configured = "https://mcp.example.test/.well-known/oauth-protected-resource"
	setupGinTestMode()
	router := gin.New()
	registerAgentAPIProbeRoutes(router, configured)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, `Bearer resource_metadata="`+configured+`"`, w.Header().Get("WWW-Authenticate"))
	require.JSONEq(t, `{"error":"authentication_required","error_description":"Use a Laisky MCP API key in the Authorization header.","resource_metadata":"`+configured+`"}`, w.Body.String())

	handler := &spaHandler{root: t.TempDir(), index: []byte("<html></html>"), resourceMetadataURL: configured}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/agent/auth", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, `Bearer resource_metadata="`+configured+`"`, w.Header().Get("WWW-Authenticate"))
	require.JSONEq(t, `{"error":"authentication_required","error_description":"Use a Laisky MCP API key in the Authorization header.","resource_metadata":"`+configured+`"}`, w.Body.String())
}

// TestSPAWarnsWhenTheMetadataDocumentNamesAnotherResource guards self-hosted
// deployments: RFC 9728 §3.3 makes clients discard a document whose resource
// differs from the origin the challenge pointed to.
func TestSPAWarnsWhenTheMetadataDocumentNamesAnotherResource(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".well-known"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".well-known", "oauth-protected-resource"), []byte(`{"resource":"https://mcp.laisky.com"}`), 0o600))

	for origin, warnings := range map[string]int{"https://mcp.laisky.com": 0, "https://mcp.example.test": 1} {
		core, observed := observer.New(zap.DebugLevel)
		logger, err := glog.NewWithName("spa-metadata", glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
		require.NoError(t, err)
		warnOnProtectedResourceMismatch(logger, root, origin)
		require.Equal(t, warnings, observed.FilterLevelExact(zap.WarnLevel).Len(), origin)
	}
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
