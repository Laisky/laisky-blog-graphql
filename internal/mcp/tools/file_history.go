package tools

import (
	"context"
	"encoding/base64"
	"strconv"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

const (
	// FileListVersionsToolName lists immutable historical snapshots.
	FileListVersionsToolName = "file_list_versions"
	// FileReadVersionToolName reads one immutable historical snapshot.
	FileReadVersionToolName = "file_read_version"
	// FileRestoreVersionToolName restores old bytes using a current live-file condition.
	FileRestoreVersionToolName = "file_restore_version"
)

// FileHistoryTool exposes history without confusing snapshot IDs and live CAS tokens.
type FileHistoryTool struct {
	name    string
	writer  FileService
	history files.HistoryReader
}

// NewFileHistoryTools creates the three tools using shared storage and project-selected writers.
func NewFileHistoryTools(writer FileService, history files.HistoryReader) ([]Tool, error) {
	if writer == nil || history == nil {
		return nil, errors.WithStack(files.NewError(files.ErrCodeSearchBackend, "file history dependencies are unavailable", false))
	}
	out := make([]Tool, 0, 3)
	for _, name := range []string{FileListVersionsToolName, FileReadVersionToolName, FileRestoreVersionToolName} {
		out = append(out, &FileHistoryTool{name: name, writer: writer, history: history})
	}
	return out, nil
}

// Definition advertises exact string IDs and mandatory restore preconditions.
func (t *FileHistoryTool) Definition() mcp.Tool {
	options := []mcp.ToolOption{
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project namespace.")),
		mcp.WithString("path", mcp.Required(), mcp.Description("Exact logical file path, including deleted files with retained history.")),
	}
	if t.name == FileListVersionsToolName {
		options = append(options,
			mcp.WithDescription("List immutable file history, newest ID first. Results are paginated; pass next_cursor as before_id. IDs are decimal strings, never live file versions."),
			mcp.WithString("before_id", mcp.Description("Optional next_cursor from the preceding page. Omit on the first page.")),
			mcp.WithNumber("limit", mcp.Description("Page size, default 50 and maximum 200."), func(p map[string]any) { p["type"], p["minimum"], p["maximum"] = "integer", 1, 200 }),
			mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true),
		)
	} else {
		options = append(options, mcp.WithString("history_id", mcp.Required(),
			mcp.Description("Exact positive decimal string ID returned by file_list_versions; not an incarnation:revision token."),
			func(p map[string]any) { p["pattern"] = `^[1-9][0-9]{0,18}$` }))
		if t.name == FileReadVersionToolName {
			options = append(options,
				mcp.WithDescription("Read one immutable history snapshot. Legacy non-UTF-8 bytes return base64 and must not be treated as UTF-8 text."),
				mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true))
		} else {
			options = append(options,
				mcp.WithDescription("Restore immutable history as a new live revision. Supply the ORIGINAL current-file expected_version, or create_only=true when absent. "+
					"A conflict has no write effect; re-read and review, never silently update the token. Uses the selected project plugin."),
				expectedFileVersionOption(), mcp.WithBoolean("create_only", mcp.Description("Restore only if the live path does not exist.")),
				fileToolPluginOption(), mcp.WithReadOnlyHintAnnotation(false), mcp.WithDestructiveHintAnnotation(true), requireFileWriteSchema())
		}
	}
	return mcp.NewTool(t.name, options...)
}

// Handle validates identity and intent before accessing history or invoking a writer.
func (t *FileHistoryTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth, ok := fileAuthFromContext(ctx)
	if !ok {
		return fileToolErrorResult(files.ErrCodePermissionDenied, "missing authorization", false), nil
	}
	project, err := req.RequireString("project")
	if err != nil {
		return fileToolErrorResult(files.ErrCodeInvalidArgument, "project is required", false), nil
	}
	path, err := req.RequireString("path")
	if err != nil {
		return fileToolErrorResult(files.ErrCodeInvalidArgument, "path is required", false), nil
	}
	path = normalizeFilePath(path)
	if err := files.ValidateProject(project); err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
	}
	if err := files.ValidatePath(path); err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
	}
	if path == "" {
		return fileToolErrorResult(files.ErrCodeInvalidPath, "history requires an exact file path", false), nil
	}
	if t.name == FileListVersionsToolName {
		return t.list(ctx, req, auth, project, path)
	}
	rawID, err := req.RequireString("history_id")
	if err != nil {
		return fileToolErrorResult(files.ErrCodeInvalidArgument, "history_id must be a decimal string", false), nil
	}
	id, err := files.ParseHistoryID(rawID)
	if err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
	}
	var writer FileService
	if t.name == FileRestoreVersionToolName {
		ctx = withFilePluginOverride(ctx, req)
		// The final operation is a selected plugin Write. Protect that exact operation,
		// not a separate preflight stat and not the immutable history row.
		writer, ctx, err = conditionalFileService(ctx, t.writer, req, auth, project, path, "", files.FileOperationWrite)
		if err != nil {
			return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
		}
	}
	snapshot, err := t.history.ReadVersion(ctx, auth, project, path, id)
	if err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
	}
	if t.name == FileRestoreVersionToolName {
		result, err := writer.Write(ctx, auth, project, path, string(snapshot.Content), "utf-8", 0, files.WriteModeTruncate)
		if err != nil {
			return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
		}
		return historyToolJSON(map[string]any{"bytes_written": result.BytesWritten, "version": result.Version})
	}
	content, encoding := string(snapshot.Content), "utf-8"
	if !utf8.Valid(snapshot.Content) {
		content, encoding = base64.StdEncoding.EncodeToString(snapshot.Content), "base64"
	}
	return historyToolJSON(map[string]any{
		"history_id": strconv.FormatUint(snapshot.ID, 10), "content": content, "content_encoding": encoding,
		"size": snapshot.Size, "created_at": snapshot.CreatedAt.UTC(),
	})
}

// list reads one bounded page and never converts identifiers through floating-point values.
func (t *FileHistoryTool) list(ctx context.Context, req mcp.CallToolRequest, auth files.AuthContext, project, path string) (*mcp.CallToolResult, error) {
	limit, err := toolpolicy.OptionalInt(req.GetArguments(), "limit", 50, 1, 200)
	if err != nil {
		return fileToolErrorResult(files.ErrCodeInvalidArgument, "limit must be an integer between 1 and 200", false), nil
	}
	var before uint64
	if raw, present := req.GetArguments()["before_id"]; present {
		value, ok := raw.(string)
		if !ok {
			return fileToolErrorResult(files.ErrCodeInvalidArgument, "before_id must be a decimal string", false), nil
		}
		before, err = files.ParseHistoryID(value)
		if err != nil {
			return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
		}
	}
	page, err := t.history.ListVersionPage(ctx, auth, project, path, before, limit)
	if err != nil {
		return fileToolErrorFromErr(err), nil //nolint:nilerr // MCP tool error
	}
	return historyToolJSON(page)
}

// historyToolJSON preserves structured data and reports encoding errors without leaking bytes.
func historyToolJSON(payload any) (*mcp.CallToolResult, error) {
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return fileToolErrorResult(files.ErrCodeSearchBackend, "failed to encode file history response", false), nil
	}
	return result, nil
}
