package tools

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// ResolveVersionedFileService selects the same version-aware backend for MCP
// and the browser HTTP editor. Unsupported plugins must not silently drop CAS.
func ResolveVersionedFileService(ctx context.Context, svc FileService, auth files.AuthContext, project string) (FileService, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is unavailable", false)
	}
	if resolver, ok := svc.(interface {
		Resolve(context.Context, files.AuthContext, string, string) (FileService, error)
	}); ok {
		resolved, err := resolver.Resolve(ctx, auth, project, "")
		if err != nil {
			return nil, err
		}
		svc = resolved
	}
	capability, ok := svc.(interface{ SupportsFileVersionPreconditions() bool })
	if !ok || !capability.SupportsFileVersionPreconditions() {
		return nil, files.NewError(files.ErrCodeInvalidArgument, "selected file backend does not support version preconditions", false)
	}
	return svc, nil
}
