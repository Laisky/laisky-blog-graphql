package pageindex

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFinalizeDocDescriptionPrefersTreeDescription covers P02: an existing PDF
// description is bounded and returned as the PageIndex summary.
func TestFinalizeDocDescriptionPrefersTreeDescription(t *testing.T) {
	tree := &Tree{
		Type:           KindPDF,
		DocDescription: "This incident review explains the retry-queue saturation event and its mitigation.",
		Structure:      []*Node{{Title: "Overview"}},
	}
	desc, source := finalizeDocDescription(tree, []byte("raw content"))
	require.Equal(t, files.SummarySourcePageIndex, source)
	require.Equal(t, "This incident review explains the retry-queue saturation event and its mitigation.", desc)
	require.LessOrEqual(t, files.SummaryWordCount(desc), files.SummaryMaxWordsHard)
}

// TestFinalizeDocDescriptionDerivesFromMarkdownTree covers P03: a Markdown tree with
// no description derives a grounded one from its section structure.
func TestFinalizeDocDescriptionDerivesFromMarkdownTree(t *testing.T) {
	tree := &Tree{
		Type: KindMarkdown,
		Structure: []*Node{
			{Title: "Introduction", Summary: "The system indexes long documents."},
			{Title: "Retrieval", Children: []*Node{{Title: "Ranking"}}},
		},
	}
	desc, source := finalizeDocDescription(tree, []byte("# Introduction\nlong doc\n# Retrieval\n"))
	require.Equal(t, files.SummarySourcePageIndex, source)
	require.Contains(t, desc, "Introduction")
	require.Contains(t, desc, "Retrieval")
	require.LessOrEqual(t, files.SummaryWordCount(desc), files.SummaryMaxWordsHard)
	require.LessOrEqual(t, len(desc), files.SummaryMaxBytesHard)
	require.NotContains(t, desc, "<")
}

// TestFinalizeDocDescriptionFallsBack covers the deterministic fallback for a tree
// with no usable description or structure.
func TestFinalizeDocDescriptionFallsBack(t *testing.T) {
	tree := &Tree{Type: KindMarkdown, Structure: nil}
	desc, source := finalizeDocDescription(tree, []byte("Plain body text about latency and retries."))
	require.Equal(t, files.SummarySourceDeterministicFallback, source)
	require.NotEmpty(t, desc)
	require.LessOrEqual(t, len(desc), files.SummaryMaxBytesHard)
}

// TestPublicDocSummaryClampsLegacyDescription ensures an over-long legacy description
// is clamped to the shared limits at search time (§4.5).
func TestPublicDocSummaryClampsLegacyDescription(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 1000 words, far over the cap
	tree := &Tree{DocDescription: long}
	got := publicDocSummary(tree)
	require.NotEmpty(t, got)
	require.LessOrEqual(t, files.SummaryWordCount(got), files.SummaryMaxWordsHard)
	require.LessOrEqual(t, len(got), files.SummaryMaxBytesHard)
}

// TestSearchLoopAttachesDocSummary covers P02/P08: every returned PageIndex chunk
// carries the bounded document description in file_summary.
func TestSearchLoopAttachesDocSummary(t *testing.T) {
	fs := newMemoryFS()
	store := NewSysStore(fs)
	ctx := context.Background()
	desc := "This document describes the alpha subsystem and its retrieval behavior."
	tree := &Tree{
		DocID:          "d1",
		Type:           KindPDF,
		PageCount:      4,
		DocDescription: desc,
		Structure:      []*Node{{Title: "Root", StartIndex: 1, EndIndex: 4}},
		Pages: []Page{
			{Page: 1, Content: "page-1"},
			{Page: 2, Content: "page-2"},
		},
	}
	require.NoError(t, store.PutTree(ctx, "p", "d1", tree))
	require.NoError(t, store.UpdateIndexEntry(ctx, "p", "/d.pdf", IndexEntry{DocID: "d1", Type: "pdf"}))

	stub := NewStubLLM()
	stub.SetDefault(TextResponse(`{"ranges":[{"start":1,"end":2,"reason":"top"}]}`))
	searcher := NewSearcher(stub, store, &Indexer{}, defaultTestSettings())
	res, err := searcher.Run(ctx, SearchInput{Project: "p", Query: "alpha", Limit: 5})
	require.NoError(t, err)
	require.NotEmpty(t, res.Chunks)
	for _, c := range res.Chunks {
		require.Equal(t, desc, c.FileSummary)
	}
}
