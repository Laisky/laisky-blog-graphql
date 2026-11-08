package web

import (
	"net/http"
	"strings"
	"time"

	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/library/httpbody"
)

// requestBodyClass names a route family that shares one request-body budget.
type requestBodyClass string

const (
	// bodyClassDefault covers every route without a declared family: small JSON
	// endpoints such as the one-api balance proxy, SSO code exchange and webhooks.
	bodyClassDefault requestBodyClass = "default"
	// bodyClassGraphQL covers the GraphQL query endpoints.
	bodyClassGraphQL requestBodyClass = "graphql"
	// bodyClassToolConsole covers /tools/<name>/ console APIs, which own their
	// byte caps (user-request images are configurable well above the default).
	bodyClassToolConsole requestBodyClass = "tool_console"
	// bodyClassMCP covers the MCP transport, whose handler chain enforces its own
	// budget, including the tighter cap for anonymous discovery.
	bodyClassMCP requestBodyClass = "mcp"
)

// Default request-body budgets of the HTTP server.
const (
	defaultBodyReadTimeout          = 60 * time.Second
	defaultBodyMaxBytes       int64 = 1 << 20
	defaultGraphQLBodyMaxByte int64 = 64 << 20
	defaultHTTPIdleTimeout          = 120 * time.Second
	defaultReadHeaderTimeout        = 10 * time.Second
	requestBodyConfigPrefix         = "settings.web.request_body."
)

// requestBodySettings is the single table of request-body budgets.
type requestBodySettings struct {
	ReadTimeout     time.Duration
	DefaultMaxBytes int64
	GraphQLMaxBytes int64
	MCP             mcp.RequestBodyBudget
}

// loadRequestBodySettings reads settings.web.request_body.* through get.
func loadRequestBodySettings(get func(string) any) requestBodySettings {
	value := func(name string) any {
		if get == nil {
			return nil
		}
		return get(requestBodyConfigPrefix + name)
	}
	settings := requestBodySettings{
		ReadTimeout:     configDuration(value("read_timeout"), defaultBodyReadTimeout),
		DefaultMaxBytes: configInt64(value("default_max_bytes"), defaultBodyMaxBytes),
		GraphQLMaxBytes: configInt64(value("graphql_max_bytes"), defaultGraphQLBodyMaxByte),
		MCP: mcp.RequestBodyBudget{
			MaxBytes:          configInt64(value("mcp_max_bytes"), mcp.DefaultMCPMaxBodyBytes),
			AnonymousMaxBytes: configInt64(value("mcp_anonymous_max_bytes"), mcp.DefaultMCPAnonymousMaxBodyBytes),
		},
	}
	if settings.ReadTimeout <= 0 {
		settings.ReadTimeout = defaultBodyReadTimeout
	}
	if settings.DefaultMaxBytes <= 0 {
		settings.DefaultMaxBytes = defaultBodyMaxBytes
	}
	if settings.GraphQLMaxBytes <= 0 {
		settings.GraphQLMaxBytes = defaultGraphQLBodyMaxByte
	}
	settings.MCP.ReadTimeout = settings.ReadTimeout
	return settings
}

// loadRequestBodySettingsFromConfig reads the budgets from the shared config.
func loadRequestBodySettingsFromConfig() requestBodySettings {
	return loadRequestBodySettings(func(key string) any { return gconfig.Shared.Get(key) })
}

// requestBodyPolicy maps gin route templates to body budgets. It is installed
// as the first middleware after panic recovery, ahead of every body reader.
type requestBodyPolicy struct {
	settings     requestBodySettings
	routes       map[string]requestBodyClass
	toolPrefixes []string
}

// newRequestBodyPolicy derives the route families from the same path helpers
// that register the routes, so classification cannot drift from registration.
func newRequestBodyPolicy(prefix urlPrefixConfig, settings requestBodySettings) *requestBodyPolicy {
	policy := &requestBodyPolicy{settings: settings, routes: make(map[string]requestBodyClass)}
	for _, path := range mcpTransportRoutePaths(prefix) {
		policy.routes[path] = bodyClassMCP
	}
	for _, path := range graphqlRoutePaths(prefix) {
		policy.routes[path] = bodyClassGraphQL
	}
	policy.toolPrefixes = []string{prefix.join("/tools/")}
	if prefix.public == "" && prefix.join("/tools/") != "/tools/" {
		policy.toolPrefixes = append(policy.toolPrefixes, "/tools/")
	}
	return policy
}

// classify returns the body family of a matched gin route template.
func (p *requestBodyPolicy) classify(fullPath string) requestBodyClass {
	if class, ok := p.routes[fullPath]; ok {
		return class
	}
	for _, toolPrefix := range p.toolPrefixes {
		if strings.HasPrefix(fullPath, toolPrefix) {
			return bodyClassToolConsole
		}
	}
	return bodyClassDefault
}

// budget returns the gin-level budget of a family.
func (p *requestBodyPolicy) budget(class requestBodyClass) httpbody.Budget {
	switch class {
	case bodyClassGraphQL:
		return httpbody.Budget{MaxBytes: p.settings.GraphQLMaxBytes, ReadTimeout: p.settings.ReadTimeout}
	case bodyClassToolConsole:
		return httpbody.Budget{ReadTimeout: p.settings.ReadTimeout}
	default:
		return httpbody.Budget{MaxBytes: p.settings.DefaultMaxBytes, ReadTimeout: p.settings.ReadTimeout}
	}
}

// middleware enforces the budget of the matched route before any handler or
// later middleware reads the body.
func (p *requestBodyPolicy) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		class := p.classify(c.FullPath())
		if class == bodyClassMCP {
			c.Next()
			return
		}
		result, err := httpbody.Enforce(c.Writer, c.Request, p.budget(class))
		if err != nil {
			c.Header("Connection", "close")
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error":       "request_too_large",
				"limit_bytes": httpbody.Limit(err),
			})
			return
		}
		if result.DeadlineErr != nil {
			ginMw.GetLogger(c).Debug("request body read deadline unavailable",
				zap.String("route_class", string(class)), zap.Error(result.DeadlineErr))
		}
		c.Next()
	}
}

// newHTTPServer configures connection timeouts. ReadTimeout and WriteTimeout
// stay unset on purpose: either would cut MCP SSE streams, held get_user_request
// long polls and GraphQL websockets. Body reads are bounded per request by
// requestBodyPolicy instead; idle keep-alive connections are reaped.
func newHTTPServer(addr string, handler http.Handler, idleTimeout time.Duration) *http.Server {
	if idleTimeout <= 0 {
		idleTimeout = defaultHTTPIdleTimeout
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// mcpDiscoveryPath is the well-known MCP transport alias.
const mcpDiscoveryPath = "/.well-known/mcp"

// mcpTransportRoutePaths lists the routes that serve the MCP transport.
func mcpTransportRoutePaths(prefix urlPrefixConfig) []string {
	return append(prefix.rootVariants(), mcpDiscoveryPath)
}

// graphqlRoutePaths lists every GraphQL query route, in registration order.
func graphqlRoutePaths(prefix urlPrefixConfig) []string {
	paths := make([]string, 0, 8)
	for _, route := range []string{"/query/", "/query/v2/"} {
		path := prefix.join(route)
		paths = append(paths, path)
		if trimmed := strings.TrimSuffix(path, "/"); trimmed != path {
			paths = append(paths, trimmed)
		}
		if prefix.public == "" {
			paths = append(paths, route, strings.TrimSuffix(route, "/"))
		}
	}
	return uniqueStrings(paths)
}

// uniqueStrings removes duplicates while keeping the first occurrence order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// configDuration accepts Go duration strings, time.Duration, or integer seconds.
func configDuration(raw any, fallback time.Duration) time.Duration {
	switch value := raw.(type) {
	case time.Duration:
		return value
	case string:
		if parsed, err := time.ParseDuration(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	case int:
		return time.Duration(value) * time.Second
	case int64:
		return time.Duration(value) * time.Second
	case float64:
		return time.Duration(value * float64(time.Second))
	}
	return fallback
}

// configInt64 coerces numeric config values.
func configInt64(raw any, fallback int64) int64 {
	switch value := raw.(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	}
	return fallback
}
