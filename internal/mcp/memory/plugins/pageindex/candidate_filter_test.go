package pageindex

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterCandidatesLimitedMatchesFullSort(t *testing.T) {
	t.Parallel()

	ix := make(Index, 1_000)
	for i := 0; i < 1_000; i++ {
		group := "alpha"
		if i%3 == 0 {
			group = "beta"
		}
		path := fmt.Sprintf("/docs/%s/%04d.md", group, 999-i)
		ix[path] = IndexEntry{DocID: fmt.Sprintf("doc-%04d", i), Type: "markdown"}
	}

	tests := []struct {
		name   string
		prefix string
		limit  int
	}{
		{name: "unlimited zero", limit: 0},
		{name: "unlimited negative", limit: -1},
		{name: "first candidate", limit: 1},
		{name: "default candidate count", limit: 5},
		{name: "wildcard", prefix: "*", limit: 7},
		{name: "matching prefix", prefix: "/docs/beta/", limit: 11},
		{name: "missing prefix", prefix: "/missing/", limit: 5},
		{name: "limit larger than index", limit: 2_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := filterCandidates(ix, tt.prefix)
			if tt.limit > 0 && len(want) > tt.limit {
				want = want[:tt.limit]
			}
			require.Equal(t, want, filterCandidatesLimited(ix, tt.prefix, tt.limit))
		})
	}
}

func TestSearcherRunCandidateLimitKeepsLexicalFirstDocuments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewSysStore(newMemoryFS())
	for i, userPath := range []string{"/z.pdf", "/b.pdf", "/a.pdf"} {
		docID := fmt.Sprintf("doc-%d", i)
		tree := &Tree{
			DocID:     docID,
			Type:      KindPDF,
			PageCount: 1,
			Structure: []*Node{{Title: "Root", StartIndex: 1, EndIndex: 1}},
			Pages:     []Page{{Page: 1, Content: userPath}},
		}
		require.NoError(t, store.PutTree(ctx, "project", docID, tree))
		require.NoError(t, store.UpdateIndexEntry(ctx, "project", userPath, IndexEntry{DocID: docID, Type: "pdf"}))
	}

	llm := NewStubLLM()
	llm.SetDefault(TextResponse(`{"ranges":[{"start":1,"end":1,"reason":"top"}]}`))
	cfg := defaultTestSettings()
	cfg.TreeQuery.CandidateDocs = 2
	cfg.TreeQuery.MaxSteps = 3
	searcher := NewSearcher(llm, store, &Indexer{}, cfg)

	result, err := searcher.Run(ctx, SearchInput{Project: "project", Query: "query", Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Chunks, 2)
	require.Equal(t, []string{"/a.pdf", "/b.pdf"}, []string{
		result.Chunks[0].FilePath,
		result.Chunks[1].FilePath,
	})
}

var benchmarkCandidateSink []rankedCandidate

func BenchmarkFilterCandidatesFullSort100K(b *testing.B) {
	ix := newCandidateBenchmarkIndex()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		candidates := filterCandidates(ix, "")
		benchmarkCandidateSink = candidates[:5]
	}
	b.StopTimer()
	require.Len(b, benchmarkCandidateSink, 5)
}

func BenchmarkFilterCandidatesLimited100K(b *testing.B) {
	ix := newCandidateBenchmarkIndex()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkCandidateSink = filterCandidatesLimited(ix, "", 5)
	}
	b.StopTimer()
	require.Len(b, benchmarkCandidateSink, 5)
}

func newCandidateBenchmarkIndex() Index {
	const size = 100_000
	ix := make(Index, size)
	for i := 0; i < size; i++ {
		userPath := fmt.Sprintf("/docs/%06d.md", size-1-i)
		ix[userPath] = IndexEntry{DocID: fmt.Sprintf("doc-%06d", i), Type: "markdown"}
	}
	return ix
}
