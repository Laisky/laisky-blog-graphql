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
