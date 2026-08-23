package pageindex

import (
	"container/heap"
	"sort"
	"strings"
)

// filterCandidatesLimited returns the lexicographically first limit entries
// from ix whose paths match prefix. A non-positive limit preserves the original
// unbounded behavior; otherwise the returned candidates are sorted by path.
func filterCandidatesLimited(ix Index, prefix string, limit int) []rankedCandidate {
	if limit <= 0 || limit >= len(ix) {
		return filterCandidates(ix, prefix)
	}

	candidates := make(candidateMaxHeap, 0, limit)
	matchAll := prefix == "" || prefix == "*"
	for userPath, entry := range ix {
		if !matchAll && !strings.HasPrefix(userPath, prefix) {
			continue
		}

		candidate := rankedCandidate{userPath: userPath, entry: entry}
		if len(candidates) < limit {
			heap.Push(&candidates, candidate)
			continue
		}
		if userPath >= candidates[0].userPath {
			continue
		}

		candidates[0] = candidate
		heap.Fix(&candidates, 0)
	}

	// Sorting only the bounded heap preserves the exact order returned by the
	// previous full sort while keeping temporary storage proportional to limit.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].userPath < candidates[j].userPath
	})
	return candidates
}

// candidateMaxHeap keeps the lexicographically largest retained path at index zero.
type candidateMaxHeap []rankedCandidate

// Len returns the number of candidates currently stored in the heap.
func (h candidateMaxHeap) Len() int { return len(h) }

// Less reports whether the candidate at i should rank above the candidate at j
// in the max-heap, placing the lexicographically largest path at the root.
func (h candidateMaxHeap) Less(i, j int) bool { return h[i].userPath > h[j].userPath }

// Swap exchanges the candidates at indices i and j.
func (h candidateMaxHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

// Push appends value to the heap and panics when value is not a rankedCandidate.
func (h *candidateMaxHeap) Push(value any) {
	candidate, ok := value.(rankedCandidate)
	if !ok {
		panic("pageindex: candidate heap received unexpected value")
	}
	*h = append(*h, candidate)
}

// Pop removes and returns the last candidate after container/heap moves the
// current root there; callers receive the removed rankedCandidate value.
func (h *candidateMaxHeap) Pop() any {
	old := *h
	last := len(old) - 1
	candidate := old[last]
	old[last] = rankedCandidate{}
	*h = old[:last]
	return candidate
}
