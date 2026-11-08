package pageindex

import "time"

// Default absolute spending and structure limits (settings.mcp.tools.memory.plugins.pageindex.limits.*).
// Every value is a fixed service-side ceiling; none is derived from the document,
// so a caller cannot raise it by adding headings, pages or table-of-contents
// entries. The defaults leave typical documents untouched: a document needs
// hundreds of leaf sections before the call ceiling applies.
const (
	// DefaultMaxNodesPerDocument caps the tree nodes of one document. Markdown
	// headings past the cap stay in the preceding section's text.
	DefaultMaxNodesPerDocument = 2000
	// DefaultMaxLLMCallsPerDocument caps the upstream calls one indexing run may
	// issue across every phase. Leaves past the cap get a local summary.
	DefaultMaxLLMCallsPerDocument = 256
	// DefaultMaxTokensPerDocument caps the tokens one indexing run may spend. The
	// pipeline's own per-node allowance still applies when it is smaller.
	DefaultMaxTokensPerDocument int64 = 2_000_000
	// DefaultBudgetWindow is the refill period of the rolling tenant and global budgets.
	DefaultBudgetWindow = time.Hour
	// DefaultTenantLLMCallsPerWindow caps one tenant's upstream calls per window.
	DefaultTenantLLMCallsPerWindow int64 = 1000
	// DefaultTenantTokensPerWindow caps one tenant's tokens per window.
	DefaultTenantTokensPerWindow int64 = 4_000_000
	// DefaultGlobalLLMCallsPerWindow caps the process's upstream calls per window.
	DefaultGlobalLLMCallsPerWindow int64 = 5000
	// DefaultGlobalTokensPerWindow caps the process's tokens per window.
	DefaultGlobalTokensPerWindow int64 = 20_000_000
	// DefaultMaxTrackedTenants caps the tenants whose rolling budget is held in memory.
	DefaultMaxTrackedTenants = 10_000
)

// LimitsSettings holds the absolute structure and spending ceilings of the plugin.
// A zero or negative value selects the documented default: a ceiling cannot be
// switched off, only raised or lowered.
type LimitsSettings struct {
	// MaxNodesPerDocument caps the tree nodes of one document.
	MaxNodesPerDocument int
	// MaxLLMCallsPerDocument caps the upstream calls of one indexing run.
	MaxLLMCallsPerDocument int
	// MaxTokensPerDocument caps the tokens of one indexing run.
	MaxTokensPerDocument int64
	// Window is the refill period of the rolling budgets.
	Window time.Duration
	// TenantLLMCallsPerWindow caps one tenant's upstream calls per Window.
	TenantLLMCallsPerWindow int64
	// TenantTokensPerWindow caps one tenant's tokens per Window.
	TenantTokensPerWindow int64
	// GlobalLLMCallsPerWindow caps every tenant's upstream calls per Window.
	GlobalLLMCallsPerWindow int64
	// GlobalTokensPerWindow caps every tenant's tokens per Window.
	GlobalTokensPerWindow int64
	// MaxTrackedTenants caps the tenants whose rolling budget is held in memory.
	MaxTrackedTenants int
}

// WithDefaults returns l with every unset or non-positive field replaced by its default.
func (l LimitsSettings) WithDefaults() LimitsSettings {
	l.MaxNodesPerDocument = positiveOr(l.MaxNodesPerDocument, DefaultMaxNodesPerDocument)
	l.MaxLLMCallsPerDocument = positiveOr(l.MaxLLMCallsPerDocument, DefaultMaxLLMCallsPerDocument)
	l.MaxTokensPerDocument = positiveOr(l.MaxTokensPerDocument, DefaultMaxTokensPerDocument)
	l.Window = positiveOr(l.Window, DefaultBudgetWindow)
	l.TenantLLMCallsPerWindow = positiveOr(l.TenantLLMCallsPerWindow, DefaultTenantLLMCallsPerWindow)
	l.TenantTokensPerWindow = positiveOr(l.TenantTokensPerWindow, DefaultTenantTokensPerWindow)
	l.GlobalLLMCallsPerWindow = positiveOr(l.GlobalLLMCallsPerWindow, DefaultGlobalLLMCallsPerWindow)
	l.GlobalTokensPerWindow = positiveOr(l.GlobalTokensPerWindow, DefaultGlobalTokensPerWindow)
	l.MaxTrackedTenants = positiveOr(l.MaxTrackedTenants, DefaultMaxTrackedTenants)
	return l
}

// positiveOr returns v when it is positive and def otherwise.
func positiveOr[T int | int64 | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

// documentTokenCeiling returns the token budget of one indexing run: the
// pipeline's per-node allowance when it is smaller, never more than the absolute
// MaxTokensPerDocument.
func (l LimitsSettings) documentTokenCeiling(pipelineAllowance int64) int64 {
	return min(max(pipelineAllowance, 0), l.WithDefaults().MaxTokensPerDocument)
}
