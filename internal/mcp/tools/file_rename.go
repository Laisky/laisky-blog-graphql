package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
)

// targetProjectKey is the optional destination project of a cross-project move.
const targetProjectKey = "target_project"

// FileRenameTool implements the file_rename MCP tool.
type FileRenameTool struct{ svc FileService }

// NewFileRenameTool constructs a FileRenameTool.
func NewFileRenameTool(svc FileService) (*FileRenameTool, error) {
	if svc == nil {
		return nil, files.NewError(files.ErrCodeSearchBackend, "file service is required", false)
	}
	return &FileRenameTool{svc: svc}, nil
}

// Definition returns the MCP metadata for file_rename.
func (t *FileRenameTool) Definition() mcp.Tool {
	return mcp.NewTool("file_rename",
		mcp.WithDescription("Rename one file using its required source expected_version. Non-overwriting moves require an absent destination. Overwriting moves also require "+
			"expected_destination_version or destination_must_not_exist=true. Set target_project to move the file into another project of the same caller. "+
			"Directory moves are not supported by file tokens."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Source project namespace.")),
		mcp.WithString("from_path", mcp.Required(), mcp.Description("Source file path.")),
		mcp.WithString("to_path", mcp.Required(), mcp.Description("Destination file path, inside target_project when it is set.")),
		mcp.WithString(targetProjectKey, mcp.Description("Optional destination project namespace for a cross-project move. "+
			"Omit it or pass an empty string to rename within project. Destination conditions and overwrite apply in this project.")),
		mcp.WithBoolean("overwrite", mcp.Description("When true, a destination version or destination_must_not_exist=true is also required.")),
		expectedFileVersionOption(true),
		mcp.WithString("expected_destination_version", mcp.Description("Version of the existing destination; requires overwrite=true.")),
		mcp.WithBoolean("destination_must_not_exist", mcp.Description("Require an absent destination. Implicit for non-overwriting moves.")),
		fileToolPluginOption(), mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the file_rename tool logic.
func (t *FileRenameTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fromPath, err := req.RequireString("from_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	toPath, err := req.RequireString("to_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	targetProject, err := readTargetProjectArg(req, project)
	if err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // argument error is encoded in the MCP tool result
	}
	fromPath, toPath = normalizeFilePath(fromPath), normalizeFilePath(toPath)
	overwrite := readBoolArg(req, "overwrite")
	ctx = withFilePluginOverride(ctx, req)
	if auth, ok := fileAuthFromContext(ctx); ok {
		conditions, svcErr := parseFilePreconditions(req, files.FileOperationRename)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // precondition error is encoded in the MCP tool result
		}
		conditions.DestinationProject = targetProject
		svc, conditionalCtx, svcErr := ConditionalFileService(ctx, t.svc, auth, project, fromPath, toPath, files.FileOperationRename, conditions)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		result, svcErr := mcpplugin.RenameAcrossProjects(conditionalCtx, svc, auth, project, fromPath, targetProject, toPath, overwrite)
		if svcErr != nil {
			return fileToolErrorFromErr(svcErr), nil //nolint:nilerr // service error is encoded in the MCP tool result
		}
		toolResult, encodeErr := mcp.NewToolResultJSON(map[string]any{"moved_count": result.MovedCount})
		if encodeErr != nil {
			return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode response", true), nil //nolint:nilerr // error is encoded in the MCP tool result
		}
		return toolResult, nil
	}
	return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
}

// readTargetProjectArg returns the destination project of a rename. An absent,
// null or empty target_project keeps the move inside project; any other
// non-string value is refused rather than silently renaming within project.
func readTargetProjectArg(req mcp.CallToolRequest, project string) (string, error) {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return project, nil
	}
	raw, present := args[targetProjectKey]
	if !present || raw == nil {
		return project, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", files.NewError(files.ErrCodeInvalidArgument, targetProjectKey+" must be a string project namespace", false)
	}
	return files.NormalizeRenameTargetProject(project, value)
}
