package eval

import (
	"math"
	"testing"
)

func TestRecallAtK(t *testing.T) {
	retrieved := []string{"a", "b", "c", "d", "e"}
	gold := []string{"b", "c", "z"}
	got := RecallAtK(retrieved, gold, 5)
	want := 2.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("recall@5 = %v, want %v", got, want)
	}
}

func TestHitAtK(t *testing.T) {
	retrieved := []string{"a", "b", "c", "d", "e"}
	if !HitAtK(retrieved, []string{"c"}, 5) {
		t.Fatalf("expected hit@5 to be true when gold doc is at rank 3")
	}
	if HitAtK(retrieved, []string{"z"}, 5) {
		t.Fatalf("expected hit@5 to be false when no gold doc is retrieved")
	}
	if HitAtK(retrieved, []string{"d"}, 3) {
		t.Fatalf("expected hit@3 to be false when only rank-4 hits gold")
	}
}

func TestMRR(t *testing.T) {
	retrieved := []string{"a", "b", "c", "d", "e"}
	got := MRR(retrieved, []string{"c"})
	want := 1.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("MRR = %v, want %v", got, want)
	}
	if MRR(retrieved, []string{"zz"}) != 0 {
		t.Fatalf("MRR with no hit must be 0")
	}
}

func TestNDCGAtK(t *testing.T) {
	// All three gold docs at top 3 → DCG/IDCG match exactly → 1.0.
	retrieved := []string{"a", "b", "c", "x", "y"}
	gold := []string{"a", "b", "c"}
	got := NDCGAtK(retrieved, gold, 5)
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("perfect ranking nDCG@5 = %v, want 1.0", got)
	}

	// One gold at rank 1 → DCG = 1, IDCG = 1 (single gold) → 1.0.
	got = NDCGAtK([]string{"a", "x", "y", "z"}, []string{"a"}, 4)
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("single-rank-1 nDCG = %v, want 1.0", got)
	}

	// Gold at rank 2 → DCG = 1/log2(3) ≈ 0.6309
	got = NDCGAtK([]string{"x", "a", "y"}, []string{"a"}, 3)
	want := 1.0 / math.Log2(3)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("rank-2 nDCG = %v, want %v", got, want)
	}
}

func TestNDCGEmptyEdgeCases(t *testing.T) {
	if NDCGAtK(nil, []string{"a"}, 5) != 0 {
		t.Fatalf("empty retrieved → nDCG must be 0")
	}
	if NDCGAtK([]string{"a"}, nil, 5) != 0 {
		t.Fatalf("empty gold → nDCG must be 0")
	}
}
