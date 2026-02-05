package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
)

// TestNormalizePostNameForQuery verifies that post names are normalized for queries.
// It accepts no parameters besides the testing handle and asserts the normalized output.
func TestNormalizePostNameForQuery(t *testing.T) {
	name, err := normalizePostNameForQuery("Hello World")
	require.NoError(t, err)
	require.Equal(t, "hello+world", name)
}

// TestSanitizePostCfg_SizeOutOfRange verifies that invalid sizes are rejected.
// It accepts no parameters besides the testing handle and asserts on error behavior.
func TestSanitizePostCfg_SizeOutOfRange(t *testing.T) {
	cfg := &dto.PostCfg{Page: 0, Size: maxPostPageSize + 1}
	_, err := sanitizePostCfg(cfg)
	require.Error(t, err)
}

// TestSanitizeEmail_Normalizes verifies that email sanitization extracts the address.
// It accepts no parameters besides the testing handle and asserts on normalized output.
func TestSanitizeEmail_Normalizes(t *testing.T) {
	email, err := sanitizeEmail("Example <user@example.com>")
	require.NoError(t, err)
	require.Equal(t, "user@example.com", email)
}
