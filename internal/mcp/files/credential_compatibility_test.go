package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gkms "github.com/Laisky/go-utils/v6/crypto/kms"
	"github.com/stretchr/testify/require"
)

const compatibilityKEK = "public-synthetic-compatibility-kek-20261013"
const compatibilityCredential = "synthetic-credential-never-used-outside-tests"

// TestCredentialCompatibilityWriter exports an actual consumer envelope for
// old/new acceptance; normal test runs create no files outside the test.
func TestCredentialCompatibilityWriter(t *testing.T) {
	p, err := NewCredentialProtector(SecuritySettings{EncryptionKEKs: map[uint16]string{1: compatibilityKEK}})
	require.NoError(t, err)
	payload, err := p.EncryptCredential(context.Background(), compatibilityCredential, []byte("synthetic-account|project|path"))
	require.NoError(t, err)
	var record gkms.EncryptedData
	require.NoError(t, record.UnmarshalFromString(payload))
	require.Equal(t, gkms.EncryptedItemVer1, record.Version)
	require.Equal(t, uint16(1), record.KekID)
	require.NotEmpty(t, record.DekID)
	require.NotContains(t, payload, compatibilityCredential)
	if path := os.Getenv("GRAPHQL_CREDENTIAL_RECORD_EVIDENCE"); path != "" {
		raw, marshalErr := json.Marshal(map[string]string{"writer": payload})
		require.NoError(t, marshalErr)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
}

// TestCredentialCompatibilityReaders reads immutable envelopes written by the
// actual old and updated consumer, and rejects wrong AAD/keys/tampering.
func TestCredentialCompatibilityReaders(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "credential_compatibility.json"))
	require.NoError(t, err)
	var fixtures map[string]string
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 2)
	p, err := NewCredentialProtector(SecuritySettings{EncryptionKEKs: map[uint16]string{1: compatibilityKEK}})
	require.NoError(t, err)
	wrongKey, err := NewCredentialProtector(SecuritySettings{EncryptionKEKs: map[uint16]string{1: "different-public-synthetic-key-20261013"}})
	require.NoError(t, err)
	ctx := context.Background()
	aad := []byte("synthetic-account|project|path")
	for name, payload := range fixtures {
		t.Run(name, func(t *testing.T) {
			plaintext, decryptErr := p.DecryptCredential(ctx, payload, aad)
			require.NoError(t, decryptErr)
			require.Equal(t, compatibilityCredential, plaintext)
			plaintext, decryptErr = p.DecryptCredential(ctx, payload, []byte("different-account|project|path"))
			require.Error(t, decryptErr)
			require.Empty(t, plaintext)
			plaintext, decryptErr = wrongKey.DecryptCredential(ctx, payload, aad)
			require.Error(t, decryptErr)
			require.Empty(t, plaintext)
			var record gkms.EncryptedData
			require.NoError(t, record.UnmarshalFromString(payload))
			require.NotEmpty(t, record.Ciphertext)
			record.Ciphertext[len(record.Ciphertext)-1] ^= 1
			tampered, marshalErr := record.MarshalToString()
			require.NoError(t, marshalErr)
			plaintext, decryptErr = p.DecryptCredential(ctx, tampered, aad)
			require.Error(t, decryptErr)
			require.Empty(t, plaintext)
		})
	}
	for _, payload := range []string{"", "not-base64", "AA=="} {
		plaintext, decryptErr := p.DecryptCredential(ctx, payload, aad)
		require.Error(t, decryptErr)
		require.Empty(t, plaintext)
	}
}
