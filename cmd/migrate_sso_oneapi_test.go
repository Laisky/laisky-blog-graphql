package cmd

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// TestDecodeLegacyPasskey verifies the migration preserves raw credential
// material and authenticator counters.
func TestDecodeLegacyPasskey(t *testing.T) {
	credentialID := []byte("credential-id")
	publicKey := []byte("public-key")
	credential, err := decodeLegacyPasskey(blogModel.PasskeyCredential{
		ID:             base64.RawURLEncoding.EncodeToString(credentialID),
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
		SignCount:      7,
		BackupEligible: true,
		Transport:      "usb,internal",
	})
	require.NoError(t, err)
	require.Equal(t, credentialID, credential.ID)
	require.Equal(t, publicKey, credential.PublicKey)
	require.EqualValues(t, 7, credential.Authenticator.SignCount)
	require.True(t, credential.Flags.BackupEligible)
	require.Len(t, credential.Transport, 2)
}
