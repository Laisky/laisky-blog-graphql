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
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
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

// TestProductionWiringRefusesDisabledValidationOutsideDebug keeps the
// accept-all opt-out out of prod mode at the enforcement point itself, so a
// copied development config cannot reopen issue #86 on a production server.
func TestProductionWiringRefusesDisabledValidationOutsideDebug(t *testing.T) {
	previous := mcpauth.Installed()
	previousEnabled := gconfig.Shared.Get("settings.mcp.auth.validation.enabled")
	previousDebug := gconfig.Shared.Get("debug")
	t.Cleanup(func() {
		mcpauth.Install(previous)
		gconfig.Shared.Set("settings.mcp.auth.validation.enabled", previousEnabled)
		gconfig.Shared.Set("debug", previousDebug)
	})

	gconfig.Shared.Set("settings.mcp.auth.validation.enabled", false)
	gconfig.Shared.Set("debug", false)
	_, err := configureMCPAuthentication(glog.Shared)
	require.ErrorContains(t, err, "development-only opt-out")
	require.Same(t, previous, mcpauth.Installed(), "a refused configuration installs nothing")

	gconfig.Shared.Set("debug", true)
	authenticator, err := configureMCPAuthentication(glog.Shared)
	require.NoError(t, err)
	require.Equal(t, "disabled", authenticator.ValidatorName(), "--debug keeps the development opt-out")
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
		return map[string]any{"settings.web.request_body.graphql_max_bytes": 5, "settings.web.request_body.read_grace": "2s",
			"settings.web.request_body.min_data_rate": 512}[key]
	})
	require.Equal(t, int64(5), settings.GraphQLMaxBytes)
	require.Equal(t, 2*time.Second, settings.ReadGrace)
	require.Equal(t, 2*time.Second, settings.MCP.ReadGrace, "one read rule covers every route family")
	require.InDelta(t, 512, settings.MCP.MinBytesPerSecond, 0)
	require.Equal(t, mcp.DefaultBodyMaxReadDuration, settings.MaxReadDuration)
	require.Equal(t, mcp.DefaultBodyMaxIdle, settings.MaxIdle)
	require.Equal(t, mcp.DefaultBodyMaxIdle, newRequestBodyPolicy(prefix, settings).budget(bodyClassGraphQL).MaxIdle)
	idle := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.max_idle": "45s"}[key]
	})
	require.Equal(t, 45*time.Second, idle.MaxIdle)
	require.Equal(t, 45*time.Second, idle.MCP.MaxIdle, "the MCP transport shares the credit bound")
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

// TestGinBodyBudgetCutsBurstThenStallThroughTheProductionChain covers the
// review's burst-then-stall case on the production middleware order: 64 KiB
// at once would have earned 16 s at the 4 KiB/s default rate, but delivered
// bytes bank at most max_idle, so the stalled GraphQL body is cut promptly.
func TestGinBodyBudgetCutsBurstThenStallThroughTheProductionChain(t *testing.T) {
	outcomes := make(chan string, 1)
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.read_grace": "200ms", "settings.web.request_body.max_idle": "400ms"}[key]
	})
	server := newBudgetedEngine(t, settings, outcomes)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	started := time.Now()
	_, err = fmt.Fprintf(conn, "POST /mcp/query/ HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		1<<20, strings.Repeat(" ", 64<<10))
	require.NoError(t, err)
	select {
	case outcome := <-outcomes:
		require.Equal(t, "read error", outcome)
		require.Less(t, time.Since(started), 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("a GraphQL body that stalled after a burst kept its connection")
	}
}

// TestGinBodyBudgetCutsSlowBodiesThroughTheProductionWriterChain proves the
// deadline is reachable through gin's writer and the logger middleware.
func TestGinBodyBudgetCutsSlowBodiesThroughTheProductionWriterChain(t *testing.T) {
	outcomes := make(chan string, 1)
	settings := loadRequestBodySettings(func(key string) any {
		return map[string]any{"settings.web.request_body.read_grace": "200ms"}[key]
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
