package userrequests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizeSearchQuery_Escape verifies that LIKE wildcards are escaped after sanitization.
// It accepts no parameters besides the testing handle and asserts on escaped output.
func TestSanitizeSearchQuery_Escape(t *testing.T) {
	query, err := sanitizeSearchQuery("100%_sure")
	require.NoError(t, err)
	escaped := escapeLike(query)
	require.Equal(t, "100\\%\\_sure", escaped)
}

// TestSanitizeCursor_Invalid verifies invalid cursors are rejected.
// It accepts no parameters besides the testing handle and asserts on error behavior.
func TestSanitizeCursor_Invalid(t *testing.T) {
	_, err := sanitizeCursor("not-a-uuid")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidCursor)
}

// TestSanitizeSearchQuery_Truncates verifies overlong search queries are trimmed and bounded.
func TestSanitizeSearchQuery_Truncates(t *testing.T) {
	query, err := sanitizeSearchQuery(strings.Repeat("你", maxSearchQueryLength+10))
	require.NoError(t, err)
	require.Len(t, []rune(query), maxSearchQueryLength)
}

// TestSanitizeRequestContent_Truncates verifies overlong request content is trimmed and bounded.
func TestSanitizeRequestContent_Truncates(t *testing.T) {
	content, err := sanitizeRequestContent(strings.Repeat("x", maxRequestContentLength+32))
	require.NoError(t, err)
	require.Len(t, content, maxRequestContentLength)
}
