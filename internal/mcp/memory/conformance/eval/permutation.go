package eval

import (
	"math"
	"math/rand/v2"

	errors "github.com/Laisky/errors/v2"
)

// PermutationResult is the output of a paired two-sided permutation test.
type PermutationResult struct {
	Mean   float64 `json:"mean"`
	PValue float64 `json:"p_value"`
	B      int     `json:"b"`
}

// PairedPermutationTest implements the Smucker / Allan / Carterette
// (CIKM '07) paired two-sided permutation test on per-query metric diffs.
// B shuffles randomly flip the sign of each diff; the returned p-value is the
// fraction of shuffles whose mean absolute value is at least the observed
// |mean(diffs)|. The observed sample is included in the count (so the
// minimum p is 1/(B+1), not 0).
func PairedPermutationTest(diffs []float64, b int) float64 {
	if len(diffs) == 0 {
		return 1.0
	}
	if b <= 0 {
		b = 10000
	}

	observed := math.Abs(meanFloat(diffs))
	rng := rand.New(rand.NewPCG(0xDEADBEEF, 0xC0FFEE))

	// Streaming sign-flip: avoid materializing B*N matrix.
	atLeast := 1 // include the observed sample
	tmp := make([]float64, len(diffs))
	for i := 0; i < b; i++ {
		for j, v := range diffs {
			if rng.Float64() < 0.5 {
				tmp[j] = -v
			} else {
				tmp[j] = v
			}
		}
		if math.Abs(meanFloat(tmp)) >= observed {
			atLeast++
		}
	}
	return float64(atLeast) / float64(b+1)
}

// Compare returns the per-query mean diff (a - baseline) and its p-value.
func Compare(a, baseline []float64) (PermutationResult, error) {
	if len(a) != len(baseline) {
		return PermutationResult{}, errors.Errorf("paired sample length mismatch: %d vs %d", len(a), len(baseline))
	}
	diffs := make([]float64, len(a))
	for i := range a {
		diffs[i] = a[i] - baseline[i]
	}
	const defaultB = 10000
	return PermutationResult{
		Mean:   meanFloat(diffs),
		PValue: PairedPermutationTest(diffs, defaultB),
		B:      defaultB,
	}, nil
}

func meanFloat(in []float64) float64 {
	if len(in) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range in {
		sum += v
	}
	return sum / float64(len(in))
}
