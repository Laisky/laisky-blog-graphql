package dao

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
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

// TestNormalizeAlertName pins the canonical alert type name contract that the
// alert rate limit and the credential lookup share: aliases that differ only in
// surrounding whitespace map to one name, and names the lookup can never match
// are errors with stable text.
func TestNormalizeAlertName(t *testing.T) {
	for _, alias := range []string{"prod_alert", " prod_alert ", "prod_alert\t", "\nprod_alert", " prod_alert\r\n"} {
		name, err := NormalizeAlertName(alias)
		require.NoError(t, err, "alias %q", alias)
		require.Equal(t, "prod_alert", name, "alias %q", alias)
	}

	name, err := NormalizeAlertName(" " + strings.Repeat("界", maxAlertNameLength) + " ")
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("界", maxAlertNameLength), name, "the limit counts runes, not bytes")

	for _, tc := range []struct{ input, want string }{
		{strings.Repeat("a", maxAlertNameLength+1), "alert name exceeds max length 100"},
		{" \t\n", "alert name is required"},
		{"prod\x00alert", "alert name contains invalid null byte"},
	} {
		_, err = NormalizeAlertName(tc.input)
		require.EqualError(t, err, tc.want)
	}

	sanitized, err := sanitizeAlertName(" prod_alert ")
	require.NoError(t, err)
	require.Equal(t, "prod_alert", sanitized, "the DAO's lookups use the same form")
}

// TestNormalizePushToken pins the canonical push token the credential directory
// and ValidateTokenForAlertType compare.
func TestNormalizePushToken(t *testing.T) {
	token, err := NormalizePushToken(" \tsynthetic-token\n")
	require.NoError(t, err)
	require.Equal(t, "synthetic-token", token)

	for _, tc := range []struct{ input, want string }{
		{strings.Repeat("t", maxPushTokenLength+1), "push token exceeds max length 64"},
		{"  ", "push token is required"},
		{"tok\x00en", "push token contains invalid null byte"},
	} {
		_, err = NormalizePushToken(tc.input)
		require.EqualError(t, err, tc.want)
	}
}

// TestAlertRefusalErrors pins the refusals a caller that decides without
// storage returns: the same text ValidateTokenForAlertType gives, matching
// ErrAlertUnauthorized, so clients and the GraphQL error classifier see no change.
func TestAlertRefusalErrors(t *testing.T) {
	notFound := AlertTypeNotFoundError("missing_alert")
	require.EqualError(t, notFound, "alert_type `missing_alert` not found: mongo: no documents in result")
	require.ErrorIs(t, notFound, ErrAlertUnauthorized)
	require.ErrorIs(t, notFound, mongoLib.ErrNoDocuments)

	mismatch := AlertTokenMismatchError("known_alert")
	require.EqualError(t, mismatch, "token invalidate for `known_alert`")
	require.ErrorIs(t, mismatch, ErrAlertUnauthorized)

	before := AlertCredentialChanges()
	alertCredentialChanges.Add(1)
	require.Equal(t, before+1, AlertCredentialChanges())
}
