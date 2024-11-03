package arweave

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecompressData(t *testing.T) {
	// Test uncompressed data
	data := []byte("hello world")
	decompressed, err := DecompressData(data)
	require.NoError(t, err)
	require.Equal(t, data, decompressed)

	// Test compressed data
	original := []byte("hello world compressed")
	compressed, err := CompressData(original)
	require.NoError(t, err)

	decompressed, err = DecompressData(compressed)
	require.NoError(t, err)
	require.Equal(t, original, decompressed)

	// Test invalid compressed data
	invalidData := append(DataPrefixEnabledGz, []byte("invalid gzip data")...)
	_, err = DecompressData(invalidData)
	require.Error(t, err)
}
