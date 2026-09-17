package tools

import (
	"context"
	"encoding/json"

	errors "github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// conditionalFileService enforces client conditions before resolving/invoking a
// backend. New tool definitions are not a trust boundary: missing conditions fail
// even when a client caches an old schema or ignores the current one.
func conditionalFileService(ctx context.Context, svc FileService, req mcp.CallToolRequest, auth files.AuthContext, project, path, destination string, operation files.FileOperation) (FileService, context.Context, error) {
	if operation == files.FileOperationDelete && path == "" {
		return nil, ctx, files.NewError(files.ErrCodePermissionDenied, "root directory cannot be deleted", false)
	}
	p, err := parseFilePreconditions(req, operation)
	if err != nil {
		return nil, ctx, err
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
	svc, err = ResolveVersionedFileService(ctx, svc, auth, project)
	if err != nil {
		return nil, ctx, err
	}
	return svc, conditionalCtx, nil
}

// parseFilePreconditions rejects null, numeric, empty, malformed and
// operation-inappropriate conditions rather than downgrading to blind writes.
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

func expectedFileVersionOption(required ...bool) mcp.ToolOption {
	opts := []mcp.PropertyOption{
		mcp.Description("Opaque version returned with file_read content (or file_stat for lifecycle operations). " +
			"Required for edits, including APPEND; use create_only=true only for creation. On VERSION_CONFLICT re-read and recompute. Optional on the first read."),
		func(property map[string]any) { property["pattern"] = `^[0-9a-f]{32}:[1-9][0-9]{0,18}$` },
	}
	if len(required) > 0 && required[0] {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithString("expected_version", opts...)
}

// requireFileWriteSchema publishes the alternative preconditions as an actual
// JSON Schema constraint, not just prose. Install after all property options.
func requireFileWriteSchema() mcp.ToolOption {
	return func(tool *mcp.Tool) {
		schema := map[string]any{
			"type": "object", "properties": tool.InputSchema.Properties, "required": tool.InputSchema.Required,
			"oneOf": []any{
				map[string]any{"required": []string{"expected_version"}, "properties": map[string]any{"create_only": map[string]any{"const": false}}},
				map[string]any{"required": []string{"create_only"}, "properties": map[string]any{"create_only": map[string]any{"const": true}},
					"not": map[string]any{"required": []string{"expected_version"}}},
			},
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			// This schema contains only static JSON values. A programming error
			// must fail startup, never advertise an unguarded fallback schema.
			panic(errors.Wrap(err, "encode mandatory file_write precondition schema"))
		}
		tool.InputSchema = mcp.ToolInputSchema{}
		tool.RawInputSchema = encoded
	}
}

func addFileVersion(payload map[string]any, version string) {
	if version != "" {
		payload["version"] = version
	}
}
