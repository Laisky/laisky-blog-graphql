package fileio

import (
	"context"
	"strconv"
	"strings"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	mcpplugin "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory/plugin"
	mcptools "github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// Shared argument and audit field names. Declared once so the resolvers, the
// audit rows and the error extensions cannot drift from each other.
const (
	fieldProject   = "project"
	fieldPath      = "path"
	fieldLimit     = "limit"
	fieldHistoryID = "history_id"
	fieldCode      = "code"
	fieldRetryable = "retryable"
)

// MemoryService is the subset of the memory service the GraphQL fields need.
// It matches the interface the MCP memory tools depend on, so both interfaces
// call the same lifecycle implementation.
type MemoryService interface {
	BeforeTurn(context.Context, files.AuthContext, mcpmemory.BeforeTurnRequest) (mcpmemory.BeforeTurnResponse, error)
	AfterTurn(context.Context, files.AuthContext, mcpmemory.AfterTurnRequest) error
	RunMaintenance(context.Context, files.AuthContext, mcpmemory.SessionRequest) error
	ListDirWithAbstract(context.Context, files.AuthContext, mcpmemory.ListDirWithAbstractRequest) (mcpmemory.ListDirWithAbstractResponse, error)
}

// Resolver adapts the shared FileIO and memory services onto GraphQL.
//
// Every dependency is optional and independently checked. MCP registration
// switches are deliberately absent: disabling an MCP tool must not remove a
// GraphQL field, and a GraphQL request never runs MCP's tool wrapper.
type Resolver struct {
	plugin     mcpplugin.Plugin
	history    files.HistoryReader
	memory     MemoryService
	calllogger *calllog.Service
}

// NewResolver builds the GraphQL adapter over the shared services. A nil
// dependency makes only the fields that need it unavailable.
func NewResolver(plugin mcpplugin.Plugin, history files.HistoryReader, memory MemoryService, calllogger *calllog.Service) *Resolver {
	return &Resolver{plugin: plugin, history: history, memory: memory, calllogger: calllogger}
}

// authorize resolves the caller identity through the shared MCP normalization,
// so tenant isolation is byte-identical on GraphQL, MCP and HTTP.
func (r *Resolver) authorize(ctx context.Context) (files.AuthContext, error) {
	var header string
	if ginCtx, ok := gmw.GetGinCtxFromStdCtx(ctx); ok && ginCtx != nil {
		header = ginCtx.GetHeader("Authorization")
	}
	authCtx, err := mcpauth.FromContextOrHeader(ctx, header)
	if err != nil || authCtx == nil {
		return files.AuthContext{}, permissionDenied()
	}
	return files.AuthContext{APIKey: authCtx.APIKey, APIKeyHash: authCtx.APIKeyHash,
		UserID: authCtx.UserID, UserIdentity: authCtx.UserIdentity}, nil
}

// logger returns the request-scoped logger named after the shared tool.
func logger(ctx context.Context, tool string) logSDK.Logger {
	return gmw.GetLogger(ctx).Named(tool)
}

// withPlugin threads the per-call backend selection exactly as MCP does.
func withPlugin(ctx context.Context, selection *models.MemoryPlugin) context.Context {
	return mcpplugin.WithOverride(ctx, pluginName(selection))
}

// pluginName maps the GraphQL enum onto the shared plugin identifiers.
func pluginName(selection *models.MemoryPlugin) string {
	if selection == nil {
		return mcpplugin.DefaultPluginAuto
	}
	switch *selection {
	case models.MemoryPluginRag:
		return mcpplugin.DefaultPluginRAG
	case models.MemoryPluginPageindex:
		return mcpplugin.DefaultPluginPageIndex
	case models.MemoryPluginAuto:
		return mcpplugin.DefaultPluginAuto
	default:
		return mcpplugin.DefaultPluginAuto
	}
}

// normalizePath applies the same canonical leading slash rule as the MCP tools.
// The empty path stays empty because it addresses the project root.
func normalizePath(path string) string {
	if path == "" || strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

// validateTarget runs the shared project and path validation before any backend
// call, so an invalid request cannot reach storage through GraphQL alone.
func validateTarget(project, path string) error {
	if err := files.ValidateProject(project); err != nil {
		return graphQLError(err, "validate project")
	}
	if err := files.ValidatePath(path); err != nil {
		return graphQLError(err, "validate path")
	}
	return nil
}

// preconditions assembles the shared conditional-mutation intent from typed
// GraphQL arguments. Presence, not a sentinel value, expresses caller intent.
func preconditions(expectedVersion *string, createOnly *bool) (files.FilePreconditions, error) {
	var p files.FilePreconditions
	if expectedVersion != nil {
		if *expectedVersion == "" {
			return p, invalidArgument("expected_version must be a non-empty opaque version string")
		}
		if err := files.ValidateFileVersion(*expectedVersion); err != nil {
			return p, graphQLError(err, "validate expected_version")
		}
		p.ExpectedVersion = *expectedVersion
	}
	if createOnly != nil {
		p.CreateOnly = *createOnly
	}
	if p.CreateOnly && p.ExpectedVersion != "" {
		return p, invalidArgument("expected_version and create_only are mutually exclusive")
	}
	return p, nil
}

// conditional resolves the versioned backend through the shared gate. Its error
// must never be downgraded into an unconditional call.
func (r *Resolver) conditional(ctx context.Context, auth files.AuthContext, project, path, destination string,
	operation files.FileOperation, p files.FilePreconditions,
) (mcpplugin.Plugin, context.Context, error) {
	svc, conditionalCtx, err := mcptools.ConditionalFileService(ctx, r.plugin, auth, project, path, destination, operation, p)
	if err != nil {
		return nil, ctx, graphQLError(err, "apply file preconditions")
	}
	return svc, conditionalCtx, nil
}

// record writes one audit row per attempted operation using the shared tool
// name and the shared redaction policy. GraphQL does not also invoke MCP's
// billing/audit wrapper, so an operation is never double-counted.
func (r *Resolver) record(ctx context.Context, log logSDK.Logger, auth files.AuthContext, tool string,
	parameters map[string]any, started time.Time, callErr error,
) {
	if r.calllogger == nil {
		return
	}
	status, message := calllog.StatusSuccess, ""
	if callErr != nil {
		status, message = calllog.StatusError, callErr.Error()
	}
	// FileIO and memory are free operations; the audit row records a zero cost
	// rather than implying a charge that no interface applies.
	if err := r.calllogger.Record(ctx, calllog.RecordInput{ToolName: tool, APIKey: auth.APIKey, Status: status,
		Cost: 0, Duration: time.Since(started), Parameters: files.RedactToolArguments(tool, parameters),
		ErrorMessage: message, OccurredAt: started}); err != nil {
		log.Warn("record call log", zap.Error(err))
	}
}

// optionalInt resolves a nullable bounded Int argument. An omitted or null
// value selects the advertised default instead of a silent zero.
func optionalInt(value *int, fallback, minimum, maximum int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if *value < minimum || *value > maximum {
		return 0, invalidArgument("value must be between " + strconv.Itoa(minimum) + " and " + strconv.Itoa(maximum))
	}
	return *value, nil
}

// entryType maps the shared file type onto the GraphQL enum.
func entryType(kind files.FileType) models.FileIOEntryType {
	if kind == files.FileTypeDirectory {
		return models.FileIOEntryTypeDirectory
	}
	return models.FileIOEntryTypeFile
}

// optionalString returns nil for an empty value so a GraphQL nullable field
// distinguishes "absent" from "empty string".
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	copied := value
	return &copied
}

// optionalTime returns nil for a zero timestamp instead of year 1.
func optionalTime(value time.Time) *library.Datetime {
	if value.IsZero() {
		return nil
	}
	return library.NewDatetimeFromTime(value.UTC())
}
