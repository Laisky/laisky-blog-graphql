package plugin

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

const (
	// DefaultPluginAuto lets callers defer to the configured manager default.
	DefaultPluginAuto = "auto"
	// DefaultPluginRAG is the Phase-1 default plugin name.
	DefaultPluginRAG = "rag"
	// DefaultPluginPageIndex reserves the future long-doc engine name in schemas.
	DefaultPluginPageIndex = "pageindex"
)

// SearchMode describes the retrieval mode exposed by a plugin.
type SearchMode string

const (
	// SearchModeHybrid represents combined lexical and semantic ranking.
	SearchModeHybrid SearchMode = "hybrid"
	// SearchModeSemantic represents embedding-driven ranking.
	SearchModeSemantic SearchMode = "semantic"
	// SearchModeLexical represents keyword or BM25 ranking.
	SearchModeLexical SearchMode = "lexical"
	// SearchModeTreeReasoning represents tree-guided retrieval over long documents.
	SearchModeTreeReasoning SearchMode = "tree-reasoning"
)

// Capabilities advertises user-visible plugin behavior differences.
type Capabilities struct {
	SearchModes      []SearchMode
	SupportsRandomIO bool
	SupportsRename   bool
	SupportsVersions bool
	AsyncIndexing    bool
	FreshnessWindow  time.Duration
	MaxPayloadBytes  int64
	Notes            string
}

// Plugin is the stable memory backend contract for MCP file tools.
type Plugin interface {
	Name() string
	Capabilities() Capabilities
	Stat(context.Context, files.AuthContext, string, string) (files.StatResult, error)
	Read(context.Context, files.AuthContext, string, string, int64, int64) (files.ReadResult, error)
	Write(context.Context, files.AuthContext, string, string, string, string, int64, files.WriteMode) (files.WriteResult, error)
	Delete(context.Context, files.AuthContext, string, string, bool) (files.DeleteResult, error)
	Rename(context.Context, files.AuthContext, string, string, string, bool) (files.RenameResult, error)
	List(context.Context, files.AuthContext, string, string, int, int) (files.ListResult, error)
	Search(context.Context, files.AuthContext, string, string, string, int) (files.SearchResult, error)
	Start(context.Context) error
	Stop(context.Context) error
}

type overrideContextKey struct{}

// WithOverride stores a per-call plugin override in context for manager-based routing.
func WithOverride(ctx context.Context, override string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	normalized := NormalizeName(override)
	if normalized == "" {
		normalized = DefaultPluginAuto
	}

	return context.WithValue(ctx, overrideContextKey{}, normalized)
}

// OverrideFromContext returns the per-call plugin override stored in context.
func OverrideFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	value, _ := ctx.Value(overrideContextKey{}).(string)
	return NormalizeName(value)
}

// NormalizeName canonicalizes plugin names used by settings and per-call overrides.
func NormalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ResolveError reports a user-correctable plugin-routing failure.
type ResolveError struct {
	Requested string
	Available []string
}

// Error returns the user-facing error message for a plugin resolution failure.
func (e *ResolveError) Error() string {
	if e == nil {
		return "plugin resolution failed"
	}

	if e.Requested == "" {
		return "plugin resolution failed"
	}

	if len(e.Available) == 0 {
		return fmt.Sprintf("plugin %q is not available", e.Requested)
	}

	return fmt.Sprintf("plugin %q is not available; available plugins: %s", e.Requested, strings.Join(e.Available, ", "))
}

// AsResolveError unwraps a plugin resolution error from an error chain.
func AsResolveError(err error) (*ResolveError, bool) {
	if err == nil {
		return nil, false
	}

	var target *ResolveError
	if stderrors.As(err, &target) {
		return target, true
	}

	return nil, false
}
