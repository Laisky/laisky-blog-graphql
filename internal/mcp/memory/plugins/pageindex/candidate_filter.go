package pageindex

import (
	"container/heap"
	"sort"
	"strings"
)

// filterCandidatesLimited keeps the lexicographically first limit matches in a
// bounded max-heap, avoiding a full-index allocation and sort on every search.
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
	// previous full sort while keeping temporary memory proportional to limit.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].userPath < candidates[j].userPath
	})
	return candidates
}

type candidateMaxHeap []rankedCandidate

func (h candidateMaxHeap) Len() int           { return len(h) }
func (h candidateMaxHeap) Less(i, j int) bool { return h[i].userPath > h[j].userPath }
func (h candidateMaxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *candidateMaxHeap) Push(value any) {
	candidate, ok := value.(rankedCandidate)
	if !ok {
		panic("pageindex: candidate heap received unexpected value")
	}
	*h = append(*h, candidate)
}

func (h *candidateMaxHeap) Pop() any {
	old := *h
	last := len(old) - 1
	candidate := old[last]
	old[last] = rankedCandidate{}
	*h = old[:last]
	return candidate
}
