package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestDedicatedHTTPDoesNotRequireMCP exercises real HTTP auth even when every MCP tool is disabled.
func TestDedicatedHTTPDoesNotRequireMCP(t *testing.T) {
	setupGinTestMode()
	for _, prefix := range []urlPrefixConfig{{internal: "/mcp", public: "/mcp"}, {internal: "/mcp", public: ""}, {internal: "", public: ""}} {
		t.Run(prefix.internal+"/"+prefix.public, func(t *testing.T) {
			router := gin.New()
			// No database method can run: each unauthenticated request must be rejected
			// by the actual handler. A missing route would return 404, not 401.
			resolver := &Resolver{args: ResolverArgs{FilesService: &files.Service{}, MCPToolsSettings: mcp.ToolsSettings{}}}
			require.NotPanics(t, func() { registerToolHTTPRoutes(router, prefix, resolver, nil, nil) })
			for _, base := range toolHTTPBases(prefix, "file_io") {
				for _, operation := range []struct{ method, path string }{
					{http.MethodGet, "/api/versions?project=p&path=/a.txt"},
					{http.MethodGet, "/api/versions/1/content?project=p&path=/a.txt"},
					{http.MethodPut, "/api/file"},
					{http.MethodPost, "/api/versions/1/restore"},
				} {
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(operation.method, base+operation.path, strings.NewReader(`{}`)))
					require.Equal(t, http.StatusUnauthorized, response.Code, operation.path)
				}
			}
		})
	}
}

// TestDedicatedHTTPMissingServiceDoesNotExposeRoutes distinguishes service absence from MCP absence.
func TestDedicatedHTTPMissingServiceDoesNotExposeRoutes(t *testing.T) {
	setupGinTestMode()
	router := gin.New()
	registerToolHTTPRoutes(router, urlPrefixConfig{}, &Resolver{}, nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/tools/file_io/api/file", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNotFound, response.Code)
}
