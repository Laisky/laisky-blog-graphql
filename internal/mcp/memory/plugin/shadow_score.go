package plugin

import (
	"context"
	"math"
	"math/rand/v2"

	errors "github.com/Laisky/errors/v2"
)

// Verdict is the head-to-head outcome from one Judge pass on a (query, A, B)
// triple. Winner is one of "A", "B", or "TIE".
type Verdict struct {
	Winner string
	Reason string
}

// Judge picks the better SearchResult for a query. Implementations must be
// deterministic given the same prompt; the ensemble layer in
// conformance/eval/judge_ensemble.go provides position-swap and majority
// voting on top.
type Judge interface {
	CompareResults(ctx context.Context, query string, a, b SearchResult) (Verdict, error)
}

// ScoreOpts tunes the offline scoring step (§7.8 step 3-4).
type ScoreOpts struct {
	// PositionSwapPercent — fraction of queries with A/B swapped before judging.
	PositionSwapPercent float64
	// MinQueries — minimum sample size before a non-stay decision is allowed.
	MinQueries int
	// WinRateThreshold — promotion win-rate floor (default 0.55 per §7.8).
	WinRateThreshold float64
	// PValueThreshold — promotion p-value ceiling (default 0.05 per §7.8).
	PValueThreshold float64
	// PermutationB — number of sign-flip shuffles for the paired test.
	PermutationB int
	// Rand — optional source for deterministic position-swap selection.
	Rand *rand.Rand
}

// ScoreResult summarizes one shadow-replay batch (§7.8 step 4).
type ScoreResult struct {
	NQueries          int     `json:"n_queries"`
	AWins             int     `json:"a_wins"`
	BWins             int     `json:"b_wins"`
	Ties              int     `json:"ties"`
	AWinRate          float64 `json:"a_win_rate"`
	BWinRate          float64 `json:"b_win_rate"`
	PValue            float64 `json:"p_value"`
	PromotionDecision string  `json:"promotion_decision"`
}

// Promotion decisions per §7.8.
const (
	DecisionPromoteB    = "promote_B"
	DecisionStayA       = "stay_A"
	DecisionInvestigate = "investigate"
)

// ScoreShadowReplay runs Judge over each record (with position-swap) and
// returns the win-rate / p-value / promotion decision per §7.8.
//
// Naming convention: A == live, B == shadow. Encoding for the paired
// permutation test: +1 if shadow won, -1 if live won, 0 if tie.
func ScoreShadowReplay(ctx context.Context, records []SearchRecord, judge Judge, opts ScoreOpts) (ScoreResult, error) {
	if judge == nil {
		return ScoreResult{}, errors.New("judge is required")
	}
	opts = applyScoreDefaults(opts)

	rng := opts.Rand
	if rng == nil {
		rng = rand.New(rand.NewPCG(0xA5A5A5A5, 0x5A5A5A5A))
	}

	diffs := make([]float64, 0, len(records))
	var aWins, bWins, ties int

	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return ScoreResult{}, errors.Wrap(err, "scoring cancelled")
		}

		swap := rng.Float64() < opts.PositionSwapPercent
		first, second := rec.LiveResult, rec.ShadowResult
		if swap {
			first, second = second, first
		}

		v, err := judge.CompareResults(ctx, rec.Query, first, second)
		if err != nil {
			return ScoreResult{}, errors.Wrapf(err, "judge query %q", rec.Query)
		}

		winner := v.Winner
		if swap {
			switch winner {
			case "A":
				winner = "B"
			case "B":
				winner = "A"
			}
		}

		switch winner {
		case "A":
			aWins++
			diffs = append(diffs, -1)
		case "B":
			bWins++
			diffs = append(diffs, 1)
		default:
			ties++
			diffs = append(diffs, 0)
		}
	}

	n := len(records)
	res := ScoreResult{NQueries: n, AWins: aWins, BWins: bWins, Ties: ties}
	if n == 0 {
		res.PValue = 1.0
		res.PromotionDecision = DecisionStayA
		return res, nil
	}
	res.AWinRate = (float64(aWins) + 0.5*float64(ties)) / float64(n)
	res.BWinRate = (float64(bWins) + 0.5*float64(ties)) / float64(n)
	res.PValue = pairedPermutationTest(diffs, opts.PermutationB)
	res.PromotionDecision = decide(res, opts)
	return res, nil
}

// applyScoreDefaults fills any zero-valued fields with the §7.8 defaults.
func applyScoreDefaults(opts ScoreOpts) ScoreOpts {
	if opts.PositionSwapPercent <= 0 {
		opts.PositionSwapPercent = 0.5
	}
	if opts.MinQueries <= 0 {
		opts.MinQueries = 50
	}
	if opts.WinRateThreshold <= 0 {
		opts.WinRateThreshold = 0.55
	}
	if opts.PValueThreshold <= 0 {
		opts.PValueThreshold = 0.05
	}
	if opts.PermutationB <= 0 {
		opts.PermutationB = 10000
	}
	return opts
}

// decide implements the §7.8 promotion gate.
func decide(res ScoreResult, opts ScoreOpts) string {
	if res.NQueries < opts.MinQueries {
		return DecisionStayA
	}
	if res.BWinRate >= opts.WinRateThreshold && res.PValue < opts.PValueThreshold {
		return DecisionPromoteB
	}
	if res.BWinRate < (1.0 - opts.WinRateThreshold) {
		return DecisionInvestigate
	}
	return DecisionStayA
}

// pairedPermutationTest mirrors conformance/eval.PairedPermutationTest. We
// re-implement it here because that package already imports the plugin
// package, so importing it back would create a cycle.
func pairedPermutationTest(diffs []float64, b int) float64 {
	if len(diffs) == 0 {
		return 1.0
	}
	if b <= 0 {
		b = 10000
	}
	observed := math.Abs(meanDiffs(diffs))
	rng := rand.New(rand.NewPCG(0xDEADBEEF, 0xC0FFEE))

	atLeast := 1
	tmp := make([]float64, len(diffs))
	for i := 0; i < b; i++ {
		for j, v := range diffs {
			if rng.Float64() < 0.5 {
				tmp[j] = -v
			} else {
				tmp[j] = v
			}
		}
		if math.Abs(meanDiffs(tmp)) >= observed {
			atLeast++
		}
	}
	return float64(atLeast) / float64(b+1)
}

func meanDiffs(in []float64) float64 {
	if len(in) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range in {
		sum += v
	}
	return sum / float64(len(in))
}
