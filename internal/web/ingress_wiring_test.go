package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ginMw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// TestProductionWiringInstallsOneAPIValidator proves the server's startup path
// installs one-api validation by default for every MCP entrypoint.
func TestProductionWiringInstallsOneAPIValidator(t *testing.T) {
	previous := mcpauth.Installed()
	t.Cleanup(func() { mcpauth.Install(previous) })

	authenticator, err := configureMCPAuthentication(glog.Shared)
	require.NoError(t, err)
	require.Same(t, authenticator, mcpauth.Installed(), "console APIs and GraphQL use the installed authenticator")
	require.Equal(t, "oneapi", authenticator.ValidatorName())
	validator, ok := authenticator.Validator().(*mcpauth.OneAPITokenValidator)
	require.True(t, ok, "production must not fall back to an accept-all validator")
	require.Equal(t, strings.TrimSuffix(oneapi.BillingAPI, "/")+mcpauth.OneAPITokenBalancePath, validator.Endpoint())
}

// TestHTTPServerTimeoutsKeepStreamsAlive pins the connection timeouts: no
// whole-request read or write timeout (they would cut SSE streams and long
// polls), a header timeout and an idle timeout.
func TestHTTPServerTimeoutsKeepStreamsAlive(t *testing.T) {
	server := newHTTPServer(":0", http.NotFoundHandler(), 0)
	require.Zero(t, server.ReadTimeout)
	require.Zero(t, server.WriteTimeout)
	require.Equal(t, defaultReadHeaderTimeout, server.ReadHeaderTimeout)
	require.Equal(t, defaultHTTPIdleTimeout, server.IdleTimeout)
}

func TestRequestBodyPolicyClassifiesRegisteredRoutes(t *testing.T) {
	prefix := urlPrefixConfig{internal: "/mcp", public: ""}
	policy := newRequestBodyPolicy(prefix, loadRequestBodySettings(nil))
	for path, class := range map[string]requestBodyClass{
		"/mcp": bodyClassMCP, "/mcp/": bodyClassMCP, "/.well-known/mcp": bodyClassMCP,
		"/mcp/query/": bodyClassGraphQL, "/mcp/query": bodyClassGraphQL, "/query/v2": bodyClassGraphQL,
		"/mcp/tools/get_user_requests/api/*path": bodyClassToolConsole, "/tools/file_io/api": bodyClassToolConsole,
		"/mcp/api/oneapi/balance": bodyClassDefault, "": bodyClassDefault,
	} {
		require.Equal(t, class, policy.classify(path), path)
	}
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.graphql_max_bytes": 5, "settings.web.request_body.read_timeout": "2s"}[key]
	})
	require.Equal(t, int64(5), settings.GraphQLMaxBytes)
	require.Equal(t, 2*time.Second, settings.ReadTimeout)
	require.Equal(t, 2*time.Second, settings.MCP.ReadTimeout, "one read budget covers every route family")
	require.Zero(t, newRequestBodyPolicy(prefix, settings).budget(bodyClassToolConsole).MaxBytes, "tool consoles own their byte caps")
}

// newBudgetedEngine mirrors the production middleware order on a real server.
func newBudgetedEngine(t *testing.T, settings requestBodySettings, outcomes chan<- string) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	prefix := urlPrefixConfig{internal: "/mcp"}
	engine := gin.New()
	engine.Use(gin.Recovery(), newRequestBodyPolicy(prefix, settings).middleware(), ginMw.NewLoggerMiddleware(ginMw.WithLogger(glog.Shared)))
	reader := func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			outcomes <- "read error"
			c.Status(http.StatusBadRequest)
			return
		}
		outcomes <- "read ok"
		c.Status(http.StatusOK)
	}
	for _, path := range graphqlRoutePaths(prefix) {
		engine.Any(path, reader)
	}
	engine.POST("/mcp/api/oneapi/balance", reader)
	engine.POST("/mcp/tools/file_io/api/*path", reader)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return server
}

func TestGinBodyBudgetRejectsOversizedBodiesPerRouteFamily(t *testing.T) {
	outcomes := make(chan string, 8)
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.graphql_max_bytes": 64, "settings.web.request_body.default_max_bytes": 16}[key]
	})
	server := newBudgetedEngine(t, settings, outcomes)
	post := func(path string, size int) int {
		response, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(strings.Repeat("x", size)))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response.StatusCode
	}
	require.Equal(t, http.StatusRequestEntityTooLarge, post("/mcp/query/", 65))
	require.Equal(t, http.StatusOK, post("/mcp/query/", 64))
	require.Equal(t, http.StatusRequestEntityTooLarge, post("/mcp/api/oneapi/balance", 17))
	require.Equal(t, http.StatusOK, post("/mcp/tools/file_io/api/files", 1<<20), "tool consoles keep their own, larger caps")
	require.Len(t, outcomes, 2, "rejected bodies never reach a handler")
}

// TestGinBodyBudgetCutsSlowBodiesThroughTheProductionWriterChain proves the
// deadline is reachable through gin's writer and the logger middleware.
func TestGinBodyBudgetCutsSlowBodiesThroughTheProductionWriterChain(t *testing.T) {
	outcomes := make(chan string, 1)
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.read_timeout": "200ms"}[key]
	})
	server := newBudgetedEngine(t, settings, outcomes)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /mcp/query/ HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"query\":")
	require.NoError(t, err)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "read error", outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled GraphQL body was not cut by the read budget")
	}
}

// TestClientAddressMiddlewareRecordsCallerSource feeds the per-source API key
// validation budget of every gin route.
func TestClientAddressMiddlewareRecordsCallerSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(clientAddressMiddleware)
	var recorded string
	engine.POST("/query", func(c *gin.Context) {
		recorded = mcpauth.ClientAddressFromContext(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/query", nil)
	request.RemoteAddr = "192.0.2.44:1234"
	engine.ServeHTTP(httptest.NewRecorder(), request)
	require.Equal(t, "192.0.2.44", recorded)
}
