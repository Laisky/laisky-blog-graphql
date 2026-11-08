package tools

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	errors "github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// conditionalFileService enforces client conditions before resolving/invoking a
// backend. New tool definitions are not a trust boundary: missing conditions fail
// even when a client caches an old schema or ignores the current one.
func conditionalFileService(ctx context.Context, svc FileService, req mcp.CallToolRequest, auth files.AuthContext, project, path, destination string, operation files.FileOperation) (FileService, context.Context, error) {
	p, err := parseFilePreconditions(req, operation)
	if err != nil {
		return nil, ctx, err
	}
	// The gate itself is transport-independent so GraphQL cannot diverge from MCP.
	return ConditionalFileService(ctx, svc, auth, project, path, destination, operation, p)
}

// parseFilePreconditions rejects null, numeric, empty, malformed and
// operation-inappropriate conditions rather than downgrading to blind writes.
func parseFilePreconditions(req mcp.CallToolRequest, operation files.FileOperation) (files.FilePreconditions, error) {
	var p files.FilePreconditions
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return p, nil
	}
	for _, name := range []string{expectedVersionKey, createOnlyKey, expectedDestinationVersionKey, "destination_must_not_exist"} {
		raw, present := args[name]
		if !present {
			continue
		}
		switch name {
		case expectedVersionKey, expectedDestinationVersionKey:
			value, ok := raw.(string)
			if !ok || value == "" {
				return p, files.NewError(files.ErrCodeInvalidArgument, name+" must be a non-empty opaque version string", false)
			}
			if err := files.ValidateFileVersion(value); err != nil {
				return p, err
			}
			if name == expectedDestinationVersionKey {
				if operation != files.FileOperationRename {
					return p, files.NewError(files.ErrCodeInvalidArgument, name+" is only valid for rename", false)
				}
				p.ExpectedDestinationVersion = value
			} else {
				p.ExpectedVersion = value
			}
		case createOnlyKey:
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

// expectedFileVersionOption declares the expected_version argument, including
// the exact incarnation:revision pattern clients must send.
func expectedFileVersionOption(required ...bool) mcp.ToolOption {
	opts := []mcp.PropertyOption{
		mcp.Description("Opaque version returned with file_read content (or file_stat for lifecycle operations). " +
			"Required for edits, including APPEND; use create_only=true only for creation. On VERSION_CONFLICT re-read and recompute. Optional on the first read."),
		func(property map[string]any) { property["pattern"] = `^[0-9a-f]{32}:[1-9][0-9]{0,18}$` },
	}
	if len(required) > 0 && required[0] {
		opts = append(opts, mcp.Required())
	}
	return mcp.WithString(expectedVersionKey, opts...)
}

// requireFileWriteSchema publishes mandatory mutation conditions for write and
// restore. Keep both root fields and complete union objects: some tool clients
// render each oneOf alternative independently and do not merge sibling fields.
func requireFileWriteSchema() mcp.ToolOption {
	return func(tool *mcp.Tool) {
		branch := func(createOnly bool, required string) map[string]any {
			properties := maps.Clone(tool.InputSchema.Properties)
			condition := maps.Clone(properties[createOnlyKey].(map[string]any))
			condition["const"] = createOnly
			properties[createOnlyKey] = condition
			return map[string]any{
				schemaTypeKey:       "object",
				schemaPropertiesKey: properties,
				schemaRequiredKey:   append(slices.Clone(tool.InputSchema.Required), required),
			}
		}
		edit := branch(false, expectedVersionKey)
		create := branch(true, createOnlyKey)
		create["not"] = map[string]any{schemaRequiredKey: []string{expectedVersionKey}}
		schema := map[string]any{
			schemaTypeKey: "object", schemaPropertiesKey: tool.InputSchema.Properties, schemaRequiredKey: tool.InputSchema.Required,
			"oneOf": []any{edit, create},
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			// This schema contains only static JSON values. A programming error
			// must fail startup, never advertise an unguarded fallback schema.
			panic(errors.Wrap(err, "encode mandatory file mutation precondition schema"))
		}
		tool.InputSchema = mcp.ToolInputSchema{}
		tool.RawInputSchema = encoded
	}
}

// addFileVersion attaches a live version token to a tool response, omitting it
// when the operation produced none.
func addFileVersion(payload map[string]any, version string) {
	if version != "" {
		payload["version"] = version
	}
}
