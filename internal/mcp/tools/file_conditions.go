package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// conditionalFileService validates optional conditions and resolves a supporting
// backend before invoking it. A backend must explicitly opt in: never silently
// drop a supplied precondition through a legacy or third-party adapter.
func conditionalFileService(ctx context.Context, svc FileService, req mcp.CallToolRequest, auth files.AuthContext, project, path, destination string, operation files.FileOperation) (FileService, context.Context, error) {
	p, err := parseFilePreconditions(req, operation)
	if err != nil {
		return nil, ctx, err
	}
	if p.Empty() {
		return svc, ctx, nil
	}
	p.DestinationPath = destination
	conditionalCtx, err := files.WithFilePreconditions(ctx, auth, project, path, operation, p)
	if err != nil {
		return nil, ctx, err
	}
	if resolver, ok := svc.(interface {
		Resolve(context.Context, files.AuthContext, string, string) (FileService, error)
	}); ok {
		svc, err = resolver.Resolve(ctx, auth, project, "")
		if err != nil {
			return nil, ctx, err
		}
	}
	capability, ok := svc.(interface{ SupportsFileVersionPreconditions() bool })
	if !ok || !capability.SupportsFileVersionPreconditions() {
		return nil, ctx, files.NewError(files.ErrCodeInvalidArgument, "selected file backend does not support version preconditions", false)
	}
	return svc, conditionalCtx, nil
}

// parseFilePreconditions is deliberately strict. Null, numeric, empty, malformed
// or operation-inappropriate conditions are errors, never unconditional writes.
func parseFilePreconditions(req mcp.CallToolRequest, operation files.FileOperation) (files.FilePreconditions, error) {
	var p files.FilePreconditions
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return p, nil
	}
	for _, name := range []string{"expected_version", "create_only", "expected_destination_version", "destination_must_not_exist"} {
		raw, present := args[name]
		if !present {
			continue
		}
		switch name {
		case "expected_version", "expected_destination_version":
			value, ok := raw.(string)
			if !ok || value == "" {
				return p, files.NewError(files.ErrCodeInvalidArgument, name+" must be a non-empty opaque version string", false)
			}
			if err := files.ValidateFileVersion(value); err != nil {
				return p, err
			}
			if name == "expected_destination_version" {
				if operation != files.FileOperationRename {
					return p, files.NewError(files.ErrCodeInvalidArgument, name+" is only valid for rename", false)
				}
				p.ExpectedDestinationVersion = value
			} else {
				p.ExpectedVersion = value
			}
		case "create_only":
			value, ok := raw.(bool)
			if !ok || operation != files.FileOperationWrite {
				return p, files.NewError(files.ErrCodeInvalidArgument, "create_only must be a boolean on file_write", false)
			}
			p.CreateOnly = value
		case "destination_must_not_exist":
			value, ok := raw.(bool)
			if !ok || operation != files.FileOperationRename {
				return p, files.NewError(files.ErrCodeInvalidArgument, "destination_must_not_exist must be a boolean on file_rename", false)
			}
			p.DestinationMustNotExist = value
		}
	}
	if p.CreateOnly && p.ExpectedVersion != "" {
		return p, files.NewError(files.ErrCodeInvalidArgument, "expected_version and create_only are mutually exclusive", false)
	}
	return p, nil
}

func expectedFileVersionOption() mcp.ToolOption {
	return mcp.WithString("expected_version", mcp.Description("Opaque version from the same file_read snapshot. Reject stale requests; re-read and recompute the edit on VERSION_CONFLICT. Omit only for deliberate unconditional operations."))
}

func addFileVersion(payload map[string]any, version string) {
	if version != "" {
		payload["version"] = version
	}
}
