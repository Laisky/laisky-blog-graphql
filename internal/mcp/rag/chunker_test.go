package rag

import "testing"

func TestParagraphChunkerSplit(t *testing.T) {
	chunker := ParagraphChunker{}
	materials := "Paragraph one. Second sentence.\n\nParagraph two that is quite long and should be split into smaller pieces because it exceeds the maximum length."
	fragments := chunker.Split(materials, 40)
	if len(fragments) == 0 {
		t.Fatalf("expected fragments")
	}
	for _, fragment := range fragments {
		if fragment.Cleaned == "" {
			t.Fatalf("empty cleaned fragment")
		}
		if len(fragment.Text) > 40 {
			t.Fatalf("fragment exceeds limit: %d", len(fragment.Text))
		}
	}
}
