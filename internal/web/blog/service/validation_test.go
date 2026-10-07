package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/dto"
	blogoneapi "github.com/Laisky/laisky-blog-graphql/internal/web/blog/oneapi"
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

func TestValidateArweaveFileID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fileID  string
		wantErr bool
	}{
		// Valid Arweave transaction IDs (43 Base64URL chars)
		{name: "valid tx id", fileID: "bNbA3TEQVL60xlgCcqdz4ZPHFZ711cvo3nKZUYn0pta", wantErr: false},
		{name: "valid with underscores and dashes", fileID: "A_B-cD0123456789012345678901234567890123abc", wantErr: false},
		{name: "all zeros 43 chars", fileID: "0000000000000000000000000000000000000000000", wantErr: false},

		// Path traversal attacks
		{name: "path traversal dotdot", fileID: "../../../etc/passwd", wantErr: true},
		{name: "path traversal encoded", fileID: "..%2F..%2F..%2Fetc%2Fpasswd", wantErr: true},

		// SSRF attacks
		{name: "absolute url injection", fileID: "http://localhost/admin", wantErr: true},
		{name: "internal ip", fileID: "http://169.254.169.254/metadata", wantErr: true},

		// Invalid formats
		{name: "empty string", fileID: "", wantErr: true},
		{name: "too short", fileID: "abc", wantErr: true},
		{name: "too long 44 chars", fileID: "bNbA3TEQVL60xlgCcqdz4ZPHFZ711cvo3nKZUYn0ptaX", wantErr: true},
		{name: "contains slash", fileID: "bNbA3TEQVL60xlgCcqdz4ZPHFZ711cvo3nKZUYn/pta", wantErr: true},
		{name: "contains dot", fileID: "bNbA3TEQVL60xlgCcqdz4ZPHFZ711cvo3nKZUYn.pta", wantErr: true},
		{name: "contains space", fileID: "bNbA3TEQVL60xlgCcqdz4ZPHFZ711cvo3nKZUYn pta", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateArweaveFileID(tc.fileID)
			if tc.wantErr {
				require.Error(t, err, "expected error for fileID %q", tc.fileID)
			} else {
				require.NoError(t, err, "unexpected error for fileID %q", tc.fileID)
			}
		})
	}
}

// TestStoreSpecificExistingCredentialBounds verifies existing MongoDB accounts
// keep the bounds they were created under while OneAPI keeps its own.
func TestStoreSpecificExistingCredentialBounds(t *testing.T) {
	mongoStore := &Blog{}
	oneAPIStore := &Blog{oneapi: &blogoneapi.Repo{}}
	password24 := strings.Repeat("p", 24)
	account60 := strings.Repeat("a", 48) + "@example.com"

	got, err := mongoStore.sanitizeExistingPassword(password24)
	require.NoError(t, err)
	require.Equal(t, password24, got)
	_, err = mongoStore.sanitizeExistingPassword(strings.Repeat("p", maxLegacyMongoPasswordLength+1))
	require.Error(t, err)
	_, err = oneAPIStore.sanitizeExistingPassword(password24)
	require.Error(t, err, "OneAPI keeps its 20-character password bound")

	got, err = mongoStore.sanitizeExistingAccount(" " + strings.ToUpper(account60) + " ")
	require.NoError(t, err)
	require.Equal(t, account60, got)
	_, err = oneAPIStore.sanitizeExistingAccount(account60)
	require.Error(t, err)
}

// TestNormalizeProviderDisplayName verifies provider display names are
// truncated rather than failing the sign-in, while invalid bytes still fail.
func TestNormalizeProviderDisplayName(t *testing.T) {
	got, err := normalizeProviderDisplayName("  Zhonghua (Laisky) Cai, with a long name  ")
	require.NoError(t, err)
	require.Equal(t, "Zhonghua (Laisky) Ca", got)
	require.LessOrEqual(t, utf8.RuneCountInString(got), maxUserDisplayNameLength)

	got, err = normalizeProviderDisplayName("雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪雪")
	require.NoError(t, err)
	require.Equal(t, maxUserDisplayNameLength, utf8.RuneCountInString(got))

	_, err = normalizeProviderDisplayName("bad\x00name")
	require.Error(t, err)
}
