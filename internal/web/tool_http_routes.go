package web

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcptools "github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// registerToolHTTPRoutes depends on application services, never successful MCP construction.
// The optional tool-name callback enriches the human-request UI; it is not an availability gate.
func registerToolHTTPRoutes(router *gin.Engine, prefix urlPrefixConfig, resolver *Resolver,
	holds *userrequests.HoldManager, availableTools func() []string,
) {
	if resolver == nil {
		return
	}
	if availableTools == nil {
		availableTools = func() []string { return []string{} }
	}
	if resolver.args.AskUserService != nil {
		handler := askuser.NewHTTPHandler(resolver.args.AskUserService, log.Logger.Named("ask_user_http"))
		for _, base := range toolHTTPBases(prefix, "ask_user") {
			router.Any(base, gin.WrapH(handler))
			router.Any(base+"/*path", gin.WrapH(http.StripPrefix(base, handler)))
		}
	}
	if resolver.args.CallLogService != nil {
		handler := calllog.NewHTTPHandler(resolver.args.CallLogService, log.Logger.Named("call_log_http"))
		registerToolAPIPaths(router, prefix, "call_log", handler)
	}
	if resolver.args.UserRequestService != nil {
		handler := userrequests.NewCombinedHTTPHandlerWithImages(resolver.args.UserRequestService, holds,
			resolver.args.UserRequestImages, log.Logger.Named("user_requests_http"), availableTools)
		registerToolAPIPaths(router, prefix, "get_user_requests", handler)
	}
	if resolver.args.FilesService != nil {
		handler := files.NewHTTPHandler(resolver.args.FilesService, log.Logger.Named("file_io_http"),
			files.WithHTTPFileWriterResolver(func(ctx context.Context, auth files.AuthContext, project string) (files.FileHTTPWriter, error) {
				return mcptools.ResolveVersionedFileService(ctx, resolver.args.MCPFileService, auth, project)
			}),
		)
		registerToolAPIPaths(router, prefix, "file_io", handler)
	}
}

// registerToolAPIPaths preserves existing prefixed/root aliases without duplicate registration.
func registerToolAPIPaths(router *gin.Engine, prefix urlPrefixConfig, name string, handler http.Handler) {
	for _, base := range toolHTTPBases(prefix, name) {
		wrapped := gin.WrapH(http.StripPrefix(base, handler))
		router.Any(base+"/api", wrapped)
		router.Any(base+"/api/*path", wrapped)
	}
}

// toolHTTPBases returns the actual server mounts; the SPA routing base is not an API prefix.
func toolHTTPBases(prefix urlPrefixConfig, name string) []string {
	base := prefix.join("/tools/" + name)
	bases := []string{base}
	root := "/tools/" + name
	if prefix.public == "" && base != root {
		bases = append(bases, root)
	}
	return bases
}
