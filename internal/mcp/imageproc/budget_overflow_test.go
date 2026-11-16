package imageproc

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/tiff"
)

// tiledTIFFHeader returns a little-endian TIFF header and first IFD for a
// w x h 8-bit grayscale image stored in tileW x tileH tiles, every tile
// declaring tileBytes bytes. The tile offsets point at the start of the file;
// the estimator never follows them.
func tiledTIFFHeader(w, h uint32, compression uint16, tileW, tileH, tileBytes uint32) []byte {
	across := (uint64(w) + uint64(tileW) - 1) / uint64(tileW)
	down := (uint64(h) + uint64(tileH) - 1) / uint64(tileH)
	tiles := uint32(across * down)
	const entries = 9
	ifd := uint32(8)
	arraysAt := ifd + 2 + entries*12 + 4
	out := []byte{'I', 'I', 42, 0}
	out = binary.LittleEndian.AppendUint32(out, ifd)
	out = binary.LittleEndian.AppendUint16(out, entries)
	entry := func(tag, typ uint16, count, value uint32) {
		out = binary.LittleEndian.AppendUint16(out, tag)
		out = binary.LittleEndian.AppendUint16(out, typ)
		out = binary.LittleEndian.AppendUint32(out, count)
		out = binary.LittleEndian.AppendUint32(out, value)
	}
	countsAt, offsetsAt := uint32(0), uint32(0)
	if tiles > 1 {
		offsetsAt = arraysAt
		countsAt = arraysAt + 4*tiles
	}
	entry(256, 4, 1, w)
	entry(257, 4, 1, h)
	entry(258, 3, 1, 8) // BitsPerSample
	entry(259, 3, 1, uint32(compression))
	entry(262, 3, 1, 1) // PhotometricInterpretation: BlackIsZero
	entry(322, 4, 1, tileW)
	entry(323, 4, 1, tileH)
	if tiles == 1 {
		entry(324, 4, 1, 0)
		entry(325, 4, 1, tileBytes)
	} else {
		entry(324, 4, tiles, offsetsAt)
		entry(325, 4, tiles, countsAt)
	}
	out = binary.LittleEndian.AppendUint32(out, 0)
	if tiles > 1 {
		for range tiles {
			out = binary.LittleEndian.AppendUint32(out, 0)
		}
		for range tiles {
			out = binary.LittleEndian.AppendUint32(out, tileBytes)
		}
	}
	return out
}

// TestTIFFTileGeometryOverflowIsNotUndercharged is the maintainer review's P2
// on the TIFF estimator (PR #94): tile dimensions are untrusted 32-bit values
// and tileW*tileH*8 (and, for PackBits, tiles*blockBytes) was computed in
// signed int64 without overflow checks, so a header could wrap the estimate
// to a negative or tiny cost. The real decoder rejects every such tile size
// before it allocates the image ("tile size is too large"), so the estimator
// may refuse them outright; it must never report a cost below what an
// unwrapped computation would give.
func TestTIFFTileGeometryOverflowIsNotUndercharged(t *testing.T) {
	// fileBound is what PackBits (64x expansion of the file) and stored
	// blocks (the file itself) can allocate whatever the tile size.
	const refused, fileBound = "refused", "file bound"
	cases := map[string]struct {
		compression  uint16
		tileW, tileH uint32
		want         string
	}{
		"deflate, product overflows":      {8, math.MaxUint32, math.MaxUint32, refused},
		"deflate, bytes overflow":         {8, math.MaxUint32, math.MaxInt32, refused},
		"deflate, wraps to a tiny cost":   {8, 1 << 31, 1 << 31, refused},
		"lzw, product overflows":          {5, math.MaxUint32, math.MaxUint32, refused},
		"lzw, wraps to a tiny cost":       {5, 1 << 31, 1 << 31, refused},
		"packbits, product overflows":     {32773, math.MaxUint32, math.MaxUint32, fileBound},
		"packbits, block bytes overflow":  {32773, math.MaxUint32, 1 << 30, fileBound},
		"packbits, wraps to a tiny cost":  {32773, 1 << 31, 1 << 31, fileBound},
		"uncompressed, product overflows": {1, math.MaxUint32, math.MaxUint32, fileBound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := tiledTIFFHeader(64, 64, tc.compression, tc.tileW, tc.tileH, 1<<20)
			rawLen := int64(len(raw))

			// Not a false positive: the decoder refuses this header.
			_, decodeErr := tiff.Decode(bytes.NewReader(raw))
			require.Error(t, decodeErr, "x/image/tiff must reject the tile size")

			cfg, err := decodeConfig(raw, mimeImageTiff)
			require.NoError(t, err)
			layout := parseTIFFLayout(raw, cfg)
			require.GreaterOrEqual(t, layout.blockPixels, int64(0), "block pixels wrapped")
			require.GreaterOrEqual(t, layout.blocks, int64(0), "block count wrapped")

			buffer := tiffBlockBufferBytes(layout, rawLen, int64(cfg.Width)*int64(cfg.Height))
			require.GreaterOrEqual(t, buffer, int64(0), "block buffer estimate wrapped")

			estimate, err := estimateDecodeBytes(raw, mimeImageTiff, cfg)
			require.NoError(t, err)
			require.Positive(t, estimate)
			_, err = NewDecodeBudget(DefaultDecodeMemoryBudgetBytes).Normalize(context.Background(), raw, "tiled.tiff")
			require.Error(t, err)

			switch tc.want {
			case refused:
				require.Greater(t, estimate, DefaultDecodeMemoryBudgetBytes,
					"a tile the decoder cannot represent must not be admitted at a wrapped cost")
				require.True(t, errs.Is(err, ErrDimensionsTooLarge), "refused before decoding, got %v", err)
			case fileBound:
				want := 2*rawLen + tiffSafeReadChunk
				if tc.compression == 32773 {
					want = growingBufferFactor * (packBitsMaxRatio*rawLen + layout.blocks*128)
				}
				require.Equal(t, want, buffer, "the file, not a wrapped tile product, bounds the buffer")
			}
		})
	}
}

// TestTIFFLargeValidTilesStayAdmitted is the negative control of the overflow
// fix: the largest tiles the decoder accepts for an image within the 8192 x
// 8192 contract keep their exact, unsaturated estimate.
func TestTIFFLargeValidTilesStayAdmitted(t *testing.T) {
	raw := tiledTIFFHeader(8192, 8192, 8, 8192, 8192, 64<<20)
	cfg, err := decodeConfig(raw, mimeImageTiff)
	require.NoError(t, err)
	layout := parseTIFFLayout(raw, cfg)
	require.True(t, layout.ok)
	require.EqualValues(t, 8192*8192, layout.blockPixels)
	require.EqualValues(t, 1, layout.blocks)
	estimate, err := estimateDecodeBytes(raw, mimeImageTiff, cfg)
	require.NoError(t, err)
	require.Less(t, estimate, int64(math.MaxInt64/2))

	padded := tiledTIFFHeader(64, 64, 32773, 1024, 1024, 1<<20)
	cfg, err = decodeConfig(padded, mimeImageTiff)
	require.NoError(t, err)
	layout = parseTIFFLayout(padded, cfg)
	require.EqualValues(t, 1024*1024, layout.blockPixels)
	require.EqualValues(t, 4*min(int64(1024*1024*8), packBitsMaxRatio*int64(len(padded))+128),
		tiffBlockBufferBytes(layout, int64(len(padded)), 64*64))
}

// TestDecodeBudgetRefusesNegativeCost pins the admission side of the same
// hardening: an estimate below zero is an estimator defect and must fail
// closed instead of being admitted as a one-byte reservation.
func TestDecodeBudgetRefusesNegativeCost(t *testing.T) {
	budget := NewDecodeBudget(1 << 20)
	release, err := budget.acquireFor(context.Background(), "", -1)
	require.Error(t, err)
	require.Nil(t, release)
	require.True(t, errs.Is(err, ErrDecodeFailed), "got %v", err)
}
