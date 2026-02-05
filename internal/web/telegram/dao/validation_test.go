package dao

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizeNotesKeyword_InvalidRegex verifies invalid regex keywords are rejected.
// It accepts no parameters besides the testing handle and asserts on error behavior.
func TestSanitizeNotesKeyword_InvalidRegex(t *testing.T) {
	_, err := sanitizeNotesKeyword("[")
	require.Error(t, err)
}

// TestSecureCompareString verifies constant-time comparison returns expected results.
// It accepts no parameters besides the testing handle and asserts on comparison outcomes.
func TestSecureCompareString(t *testing.T) {
	require.True(t, secureCompareString("token", "token"))
	require.False(t, secureCompareString("token", "other"))
}
