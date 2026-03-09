package redis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewHTMLCrawlerTaskDefaultsToMarkdown verifies the default task payload requests markdown output.
func TestNewHTMLCrawlerTaskDefaultsToMarkdown(t *testing.T) {
	t.Parallel()

	task := NewHTMLCrawlerTask("https://example.com")
	require.True(t, task.OutputMarkdown)
}

// TestNewHTMLCrawlerTaskWithOptionsAllowsRawHTML verifies callers can still explicitly request raw HTML.
func TestNewHTMLCrawlerTaskWithOptionsAllowsRawHTML(t *testing.T) {
	t.Parallel()

	task := NewHTMLCrawlerTaskWithOptions("https://example.com", "", false)
	require.False(t, task.OutputMarkdown)
}
