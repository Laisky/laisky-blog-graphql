package files

import (
	"context"
	"testing"

	gkms "github.com/Laisky/go-utils/v6/crypto/kms"
	"github.com/stretchr/testify/require"
)

// TestNewCredentialProtectorValidKey ensures valid long keys are accepted.
func TestNewCredentialProtectorValidKey(t *testing.T) {
	settings := SecuritySettings{EncryptionKEKs: map[uint16]string{1: "this is a very long and secure encryption key"}}
	protector, err := NewCredentialProtector(settings)
	require.NoError(t, err)
	require.NotNil(t, protector)
}

// TestNewCredentialProtectorInvalidLength ensures keys shorter than or equal to 16 fail.
func TestNewCredentialProtectorInvalidLength(t *testing.T) {
	settings := SecuritySettings{EncryptionKEKs: map[uint16]string{1: "0123456789abcdef"}} // length 16
	protector, err := NewCredentialProtector(settings)
	require.Error(t, err)
	require.Nil(t, protector)
}

// TestNewCredentialProtectorMultiKEKs verifies multi-KEK settings prefer the largest KEK ID.
func TestNewCredentialProtectorMultiKEKs(t *testing.T) {
	settings := SecuritySettings{EncryptionKEKs: map[uint16]string{
		3: "this-is-a-long-secret-for-kek-id-3",
		8: "this-is-a-long-secret-for-kek-id-8",
	}}

	protector, err := NewCredentialProtector(settings)
	require.NoError(t, err)
	require.NotNil(t, protector)

	payload, err := protector.EncryptCredential(context.Background(), "token", []byte("aad"))
	require.NoError(t, err)

	var encrypted gkms.EncryptedData
	require.NoError(t, encrypted.UnmarshalFromString(payload))
	require.Equal(t, uint16(8), encrypted.KekID)
	require.NotEmpty(t, encrypted.DekID)
}

// TestSecuritySettingsKEKsFiltersEmptyValues verifies empty KEK values are ignored.
func TestSecuritySettingsKEKsFiltersEmptyValues(t *testing.T) {
	settings := SecuritySettings{EncryptionKEKs: map[uint16]string{
		2: "",
		3: "this-is-a-long-secret-for-kek-id-3",
	}}

	keks := settings.KEKs()
	require.Len(t, keks, 1)
	require.Equal(t, "this-is-a-long-secret-for-kek-id-3", keks[3])
}
