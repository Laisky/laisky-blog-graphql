package tools

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// ConditionalFileService applies the shared client-precondition contract for any
// external transport. MCP passes conditions parsed from tool arguments and
// GraphQL passes conditions parsed from typed field arguments; both reach the
// same gate so one interface cannot silently downgrade to a blind mutation.
//
// It returns the backend to invoke and the context that carries the conditions.
// A caller that receives an error must not fall back to an unconditional call.
func ConditionalFileService(ctx context.Context, svc FileService, auth files.AuthContext,
	project, path, destination string, operation files.FileOperation, p files.FilePreconditions,
) (FileService, context.Context, error) {
	if operation == files.FileOperationDelete && path == "" {
		return nil, ctx, files.NewError(files.ErrCodePermissionDenied, "root directory cannot be deleted", false)
	}
	if err := files.RequireClientFilePreconditions(operation, p); err != nil {
		return nil, ctx, err
	}
	if p.Empty() { // Initial read only; no mutation can pass this branch.
		return svc, ctx, nil
	}
	p.DestinationPath = destination
	conditionalCtx, err := files.WithFilePreconditions(ctx, auth, project, path, operation, p)
	if err != nil {
		return nil, ctx, err
	}
	resolved, err := ResolveVersionedFileService(ctx, svc, auth, project)
	if err != nil {
		return nil, ctx, err
	}
	return resolved, conditionalCtx, nil
}
