package files

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewCredentialProtectorValidKey ensures valid long keys are accepted.
func TestNewCredentialProtectorValidKey(t *testing.T) {
	settings := SecuritySettings{EncryptionKey: "this is a very long and secure encryption key"}
	protector, err := NewCredentialProtector(settings)
	require.NoError(t, err)
	require.NotNil(t, protector)
}

// TestNewCredentialProtectorInvalidLength ensures keys shorter than or equal to 16 fail.
func TestNewCredentialProtectorInvalidLength(t *testing.T) {
	settings := SecuritySettings{EncryptionKey: "0123456789abcdef"} // length 16
	protector, err := NewCredentialProtector(settings)
	require.Error(t, err)
	require.Nil(t, protector)
}
