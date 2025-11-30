package rag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParagraphChunkerSplit(t *testing.T) {
	chunker := ParagraphChunker{}
	materials := "Paragraph one. Second sentence.\n\nParagraph two that is quite long and should be split into smaller pieces because it exceeds the maximum length."
	fragments := chunker.Split(materials, 40)
	require.NotEmpty(t, fragments, "expected fragments")
	for _, fragment := range fragments {
		require.NotEmpty(t, fragment.Cleaned, "empty cleaned fragment")
		require.LessOrEqual(t, len(fragment.Text), 40, "fragment exceeds limit: %d", len(fragment.Text))
	}
}
