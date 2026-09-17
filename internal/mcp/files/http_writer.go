package files

import (
	"context"

	errors "github.com/Laisky/errors/v2"
)

// FileHTTPWriter is the mutation contract consumed by the HTTP editor. Production
// wiring resolves a version-aware MCP plugin, not an alternate raw-storage path.
type FileHTTPWriter interface {
	Write(context.Context, AuthContext, string, string, string, string, int64, WriteMode) (WriteResult, error)
}

// FileHTTPWriterResolver resolves a backend that enforces context preconditions.
// An error must not cause a fallback to an unselected plugin or raw storage.
type FileHTTPWriterResolver func(context.Context, AuthContext, string) (FileHTTPWriter, error)

// FileHTTPOption configures the existing authenticated FileIO HTTP handler.
type FileHTTPOption func(*filesHTTPHandler)

// WithHTTPFileWriterResolver routes saves and restores through the same plugin
// selection and mutation pipeline as MCP file_write. History reads stay in the
// authoritative, tenant-scoped immutable history store.
func WithHTTPFileWriterResolver(resolve FileHTTPWriterResolver) FileHTTPOption {
	return func(h *filesHTTPHandler) {
		if resolve == nil {
			h.writerResolver = func(context.Context, AuthContext, string) (FileHTTPWriter, error) {
				return nil, NewError(ErrCodeSearchBackend, "file writer resolver is unavailable", false)
			}
			return
		}
		h.writerResolver = resolve
	}
}

func (h *filesHTTPHandler) writeHTTPFile(ctx context.Context, auth AuthContext, project, path, content string) (WriteResult, error) {
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationWrite)
	if err := RequireClientFilePreconditions(FileOperationWrite, conditions); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	var writer FileHTTPWriter = h.service
	if h.writerResolver != nil {
		resolved, err := h.writerResolver(ctx, auth, project)
		if err != nil {
			return WriteResult{}, errors.WithStack(err)
		}
		if resolved == nil {
			return WriteResult{}, errors.WithStack(NewError(ErrCodeSearchBackend, "resolved file writer is unavailable", false))
		}
		writer = resolved
	}
	return writer.Write(ctx, auth, project, path, content, "utf-8", 0, WriteModeTruncate)
}

func (h *filesHTTPHandler) restoreHTTPFile(ctx context.Context, auth AuthContext, project, path string, id uint64) (WriteResult, error) {
	conditions := filePreconditionsFromContext(ctx, auth, project, path, FileOperationRestore)
	if err := RequireClientFilePreconditions(FileOperationRestore, conditions); err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	if h.writerResolver == nil {
		// Standalone storage handlers retain the service's atomic restore. The web
		// server always supplies a plugin resolver and takes the routed path below.
		return h.service.RestoreVersion(ctx, auth, project, path, id)
	}
	version, err := h.service.ReadVersion(ctx, auth, project, path, id)
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	// History bytes are immutable. Read them without holding a database lock over
	// plugin work; the final write atomically validates the ORIGINAL live token.
	// Rebind the operation, not the token: Restore and Write contexts are distinct.
	writeCtx, err := WithFilePreconditions(ctx, auth, project, path, FileOperationWrite, conditions)
	if err != nil {
		return WriteResult{}, errors.WithStack(err)
	}
	return h.writeHTTPFile(writeCtx, auth, project, path, string(version.Content))
}
