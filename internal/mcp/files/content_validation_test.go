package files

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeContentEncoding verifies supported encoding normalization rules.
func TestNormalizeContentEncoding(t *testing.T) {
	encoding, err := NormalizeContentEncoding("")
	require.NoError(t, err)
	require.Equal(t, "utf-8", encoding)

	encoding, err = NormalizeContentEncoding("UTF-8")
	require.NoError(t, err)
	require.Equal(t, "utf-8", encoding)

	_, err = NormalizeContentEncoding("base64")
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodeInvalidQuery))
}

// TestValidatePayloadSize verifies payload limit enforcement.
func TestValidatePayloadSize(t *testing.T) {
	require.NoError(t, ValidatePayloadSize(10, 10))
	err := ValidatePayloadSize(11, 10)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePayloadTooLarge))
}

// TestValidateFileSize verifies single-file limit enforcement.
func TestValidateFileSize(t *testing.T) {
	require.NoError(t, ValidateFileSize(10, 10))
	err := ValidateFileSize(11, 10)
	require.Error(t, err)
	require.True(t, IsCode(err, ErrCodePayloadTooLarge))
}
