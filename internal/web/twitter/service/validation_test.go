package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dto"
)

// TestSanitizeLoadTweetArgs_Defaults verifies default sort behavior.
// It accepts no parameters besides the testing handle and asserts on default values.
func TestSanitizeLoadTweetArgs_Defaults(t *testing.T) {
	cfg := &dto.LoadTweetArgs{Page: 0, Size: 10}
	out, err := sanitizeLoadTweetArgs(cfg)
	require.NoError(t, err)
	require.Equal(t, "created_at", out.SortBy)
	require.Equal(t, "DESC", out.SortOrder)
}

// TestSanitizeLoadTweetArgs_InvalidSort verifies invalid sort order is rejected.
// It accepts no parameters besides the testing handle and asserts on error behavior.
func TestSanitizeLoadTweetArgs_InvalidSort(t *testing.T) {
	cfg := &dto.LoadTweetArgs{Page: 0, Size: 10, SortOrder: "INVALID"}
	_, err := sanitizeLoadTweetArgs(cfg)
	require.Error(t, err)
}

// TestSanitizeTweetID_NonNumeric verifies non-numeric tweet IDs are rejected.
// It accepts no parameters besides the testing handle and asserts on error behavior.
func TestSanitizeTweetID_NonNumeric(t *testing.T) {
	_, err := sanitizeTweetID("abc123")
	require.Error(t, err)
}
