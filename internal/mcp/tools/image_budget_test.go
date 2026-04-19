package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanInlineBudget_OneFits(t *testing.T) {
	cfg := ImageBudgetConfig{PerCallBudgetBytes: 80 * 1024, PerImageInlineMax: 40 * 1024}
	out := PlanInlineBudget([]int{50 * 1024}, cfg)
	require.Len(t, out, 1)
	require.False(t, out[0].Inline, "50KB exceeds per-image ceiling")
}

func TestPlanInlineBudget_TwoLargerPerImage(t *testing.T) {
	cfg := ImageBudgetConfig{PerCallBudgetBytes: 80 * 1024, PerImageInlineMax: 40 * 1024}
	out := PlanInlineBudget([]int{60 * 1024, 60 * 1024}, cfg)
	require.False(t, out[0].Inline)
	require.False(t, out[1].Inline)
}

func TestPlanInlineBudget_ThreeSmall(t *testing.T) {
	cfg := ImageBudgetConfig{PerCallBudgetBytes: 80 * 1024, PerImageInlineMax: 40 * 1024}
	out := PlanInlineBudget([]int{30 * 1024, 30 * 1024, 30 * 1024}, cfg)
	require.True(t, out[0].Inline)
	require.True(t, out[1].Inline)
	require.False(t, out[2].Inline, "third would overflow call-level budget")
}

func TestPlanInlineBudget_SmallThenHuge(t *testing.T) {
	cfg := ImageBudgetConfig{PerCallBudgetBytes: 80 * 1024, PerImageInlineMax: 40 * 1024}
	out := PlanInlineBudget([]int{20 * 1024, 500 * 1024}, cfg)
	require.True(t, out[0].Inline)
	require.False(t, out[1].Inline)
}

func TestPlanInlineBudget_Empty(t *testing.T) {
	out := PlanInlineBudget(nil, DefaultImageBudget())
	require.Empty(t, out)
}
