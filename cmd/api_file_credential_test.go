package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestBuildFileCredentialProtectorEmptyKey verifies empty encryption key keeps credential protection optional.
func TestBuildFileCredentialProtectorEmptyKey(t *testing.T) {
	protector, err := buildFileCredentialProtector(files.Settings{})
	require.NoError(t, err)
	require.Nil(t, protector)
}

// TestBuildFileCredentialProtectorInvalidKey verifies invalid configured encryption key fails fast.
func TestBuildFileCredentialProtectorInvalidKey(t *testing.T) {
	settings := files.Settings{
		Security: files.SecuritySettings{
			EncryptionKEKs: map[uint16]string{1: "too-short"},
		},
	}

	protector, err := buildFileCredentialProtector(settings)
	require.Error(t, err)
	require.Nil(t, protector)
	require.Contains(t, err.Error(), "must be longer than 16 characters")
}

// TestBuildFileCredentialProtectorValidKey verifies a compliant encryption key initializes credential protection.
func TestBuildFileCredentialProtectorValidKey(t *testing.T) {
	settings := files.Settings{
		Security: files.SecuritySettings{
			EncryptionKEKs: map[uint16]string{1: "this-key-is-longer-than-16"},
		},
	}

	protector, err := buildFileCredentialProtector(settings)
	require.NoError(t, err)
	require.NotNil(t, protector)
}
