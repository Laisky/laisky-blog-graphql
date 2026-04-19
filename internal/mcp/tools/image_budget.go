package tools

// ImageBudgetConfig configures the §3.8 inline-base64 budget algorithm.
type ImageBudgetConfig struct {
	// PerCallBudgetBytes is the total base64 budget for a single MCP response.
	PerCallBudgetBytes int
	// PerImageInlineMax is the per-image ceiling; images whose base64 would
	// exceed this are emitted as resource_link only.
	PerImageInlineMax int
}

// DefaultImageBudget returns the budget limits named in the proposal.
func DefaultImageBudget() ImageBudgetConfig {
	return ImageBudgetConfig{
		PerCallBudgetBytes: 80 * 1024,
		PerImageInlineMax:  40 * 1024,
	}
}

// ImageDecision describes how a single image should be emitted.
type ImageDecision struct {
	Index  int
	Inline bool
}

// PlanInlineBudget returns per-image decisions honoring the proposal's
// greedy algorithm: inline when the per-image ceiling and remaining budget
// both allow; otherwise emit only the resource_link.
func PlanInlineBudget(base64Sizes []int, cfg ImageBudgetConfig) []ImageDecision {
	decisions := make([]ImageDecision, 0, len(base64Sizes))
	remaining := cfg.PerCallBudgetBytes
	for i, size := range base64Sizes {
		inline := false
		ceiling := cfg.PerImageInlineMax
		if ceiling <= 0 || size <= ceiling {
			if size <= remaining {
				inline = true
				remaining -= size
			}
		}
		decisions = append(decisions, ImageDecision{Index: i, Inline: inline})
	}
	return decisions
}
