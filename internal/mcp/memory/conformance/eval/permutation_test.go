package eval

import "testing"

func TestPairedPermutationConstantPositive(t *testing.T) {
	// All-positive constant diffs of length 5: every sign flip lowers the
	// absolute mean below the observed |1.0|, so the only "≥ observed" outcome
	// is the all-positive shuffle. The observed sample is included once, so
	// the smallest possible p-value is small but > 0.
	p := PairedPermutationTest([]float64{1, 1, 1, 1, 1}, 1000)
	if p > 0.05 {
		t.Fatalf("all-positive diffs returned p = %v, want < 0.05", p)
	}
}

func TestPairedPermutationBalancedZero(t *testing.T) {
	// Symmetric diffs: observed mean is large in magnitude but many shuffles
	// reach the same magnitude — p-value should be well above the threshold.
	p := PairedPermutationTest([]float64{1, -1, 1, -1, 1}, 1000)
	if p < 0.5 {
		t.Fatalf("balanced diffs returned p = %v, want > 0.5", p)
	}
}

func TestCompare(t *testing.T) {
	res, err := Compare([]float64{0.6, 0.7, 0.8}, []float64{0.5, 0.6, 0.7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Mean <= 0 {
		t.Fatalf("Compare mean = %v, expected positive", res.Mean)
	}
	if res.B != 10000 {
		t.Fatalf("Compare B = %d, want 10000", res.B)
	}
}

func TestCompareLengthMismatch(t *testing.T) {
	if _, err := Compare([]float64{1, 2}, []float64{1}); err == nil {
		t.Fatalf("expected error on length mismatch")
	}
}
