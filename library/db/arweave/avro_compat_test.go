package arweave

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/everFinance/goar/types"
	"github.com/everFinance/goar/utils"
	"github.com/linkedin/goavro/v2"
	"github.com/stretchr/testify/require"
)

// TestLegacyAvroCompatibility compares the real goar entry points with an independent codec.
func TestLegacyAvroCompatibility(t *testing.T) {
	codec, err := goavro.NewCodec(`{"type":"array","items":{"type":"record","name":"Tag","fields":[{"name":"name","type":"string"},{"name":"value","type":"string"}]}}`)
	require.NoError(t, err)
	for _, tags := range [][]types.Tag{
		{{Name: "Content-Type", Value: "text/plain"}},
		{{Name: "language", Value: "English 中文"}, {Name: "empty", Value: ""}},
		{{Name: "same", Value: "first"}, {Name: "same", Value: "second"}},
	} {
		native := make([]any, len(tags))
		for i, tag := range tags {
			native[i] = map[string]any{"name": tag.Name, "value": tag.Value}
		}
		expected, encodeErr := codec.BinaryFromNative(nil, native)
		require.NoError(t, encodeErr)
		encoded, encodeErr := utils.SerializeTags1(tags)
		require.NoError(t, encodeErr)
		require.Equal(t, expected, encoded)
		decoded, decodeErr := utils.DeserializeTags1(expected)
		require.NoError(t, decodeErr)
		require.Equal(t, tags, decoded)
	}
	empty, err := utils.SerializeTags1(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// TestLegacyAvroRejectsMalformedLengths protects the decoder replacement's bounded failure behavior.
func TestLegacyAvroRejectsMalformedLengths(t *testing.T) {
	for _, count := range []int64{1, math.MaxInt64, math.MinInt64} {
		data := binary.AppendVarint(nil, count)
		require.NotPanics(t, func() {
			_, err := utils.DeserializeTags1(data)
			require.Error(t, err, "truncated block count %d", count)
		})
	}
	_, err := utils.DeserializeTags1([]byte{2, 2})
	require.Error(t, err, "a string length without string data must be rejected")
}
