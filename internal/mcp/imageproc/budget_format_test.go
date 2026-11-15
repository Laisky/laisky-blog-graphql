package imageproc

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

// The tests in this file reproduce the review's decoder-estimation gaps (PR
// #94): each pins what the real decoder does with a crafted layout (so the
// finding is not a false positive) and that the estimate charges for it.

// pngChunk returns one CRC-valid PNG chunk.
func pngChunk(kind string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, kind...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(kind), data...)))
}

// insertPNGTRNSAfterChunks inserts n empty private ancillary chunks and then
// a grayscale tRNS chunk right after IHDR. The decoder skips the ancillary
// chunks and still applies the transparency.
func insertPNGTRNSAfterChunks(raw []byte, n int) []byte {
	const ihdrEnd = 8 + 8 + 13 + 4
	out := append([]byte(nil), raw[:ihdrEnd]...)
	for range n {
		out = append(out, pngChunk("prVt", nil)...)
	}
	out = append(out, pngChunk("tRNS", []byte{0, 0})...)
	return append(out, raw[ihdrEnd:]...)
}

// estimateFor runs MIME detection, header decoding and the estimator.
func estimateFor(t *testing.T, raw []byte, name string) int64 {
	t.Helper()
	mime, err := detectMIME(raw, name)
	require.NoError(t, err)
	cfg, err := decodeConfig(raw, mime)
	require.NoError(t, err)
	estimate, err := estimateDecodeBytes(raw, mime, cfg)
	require.NoError(t, err)
	return estimate
}

// TestPNGTransparencyBeyondChunkScanLimit is the review's P1 on the PNG
// estimator: the tRNS scan stopped after 1024 chunks and reported "no
// transparency", although image/png decodes such a grayscale PNG as NRGBA
// (4 bytes per pixel instead of 1). A scan that cannot see the IDAT must not
// be taken as proof that there is no tRNS.
func TestPNGTransparencyBeyondChunkScanLimit(t *testing.T) {
	base := encodeFixture(t, "png", patternImage("gray", 64, 48))
	late := insertPNGTRNSAfterChunks(base, 1025)

	decoded, err := png.Decode(bytes.NewReader(late))
	require.NoError(t, err)
	require.IsType(t, &image.NRGBA{}, decoded, "the decoder must apply the late tRNS (rules out a false positive)")

	early := insertPNGTRNS(base)
	require.Equal(t, estimateFor(t, early, "early.png"), estimateFor(t, late, "late.png"),
		"a tRNS after many chunks costs what an early one costs")
	require.Greater(t, estimateFor(t, late, "late.png"), estimateFor(t, base, "base.png"))

	// An unreadable chunk walk is charged as transparent, never as gray.
	header := pngHeader(MaxDecodeDimension, MaxDecodeDimension, 8, 0, 0)
	truncatedWalk := append([]byte(nil), header[:8+8+13+4]...)
	truncatedWalk = binary.BigEndian.AppendUint32(truncatedWalk, 0xFFFFFFF0)
	truncatedWalk = append(truncatedWalk, "prVt"...)
	cfg := image.Config{ColorModel: color.GrayModel, Width: MaxDecodeDimension, Height: MaxDecodeDimension}
	require.EqualValues(t, 4, pngBytesPerPixel(truncatedWalk, cfg))
	require.EqualValues(t, 1, pngBytesPerPixel(header, cfg), "a walk that reaches IDAT without tRNS stays gray")
}

// insertJPEGBeforeEOI inserts segment right before the final EOI marker.
func insertJPEGBeforeEOI(t *testing.T, raw, segment []byte) []byte {
	t.Helper()
	require.Equal(t, []byte{0xFF, 0xD9}, raw[len(raw)-2:])
	out := append([]byte(nil), raw[:len(raw)-2]...)
	out = append(out, segment...)
	return append(out, 0xFF, 0xD9)
}

// adobeAPP14 is an Adobe APP14 segment with color transform 0 ("unknown",
// which image/jpeg treats as RGB for three components).
var adobeAPP14 = []byte{0xFF, 0xEE, 0x00, 0x0E, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0}

// TestJPEGAdobeMarkerAfterFirstScan is the review's P2 on the JPEG marker
// scan: it stopped at the first SOS, but image/jpeg processes an APP14
// segment anywhere before EOI and then converts the planes to a full RGBA
// copy (4 bytes per pixel) the estimate did not count.
func TestJPEGAdobeMarkerAfterFirstScan(t *testing.T) {
	plain := encodeFixture(t, "jpeg", patternImage("rgba", 64, 48))
	late := insertJPEGBeforeEOI(t, plain, adobeAPP14)
	early := append(append([]byte{0xFF, 0xD8}, adobeAPP14...), plain[2:]...)

	for name, raw := range map[string][]byte{"late": late, "early": early} {
		decoded, err := jpeg.Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		require.IsType(t, &image.RGBA{}, decoded, "%s APP14 must trigger the RGBA conversion (rules out a false positive)", name)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(plain))
	require.NoError(t, err)
	require.IsType(t, &image.YCbCr{}, decoded)

	require.Equal(t, estimateFor(t, early, "early.jpg"), estimateFor(t, late, "late.jpg"))
	require.Equal(t, estimateFor(t, plain, "plain.jpg")+4*64*48, estimateFor(t, late, "late.jpg"))
}

// TestWebPExtendedLosslessIsChargedAsLossless is the review's P2 on the WebP
// estimator: for an extended (VP8X) file without the alpha flag DecodeConfig
// reports YCbCr even when the image chunk is lossless VP8L, which decodes to
// NRGBA plus an inverse-transform copy. The estimate must follow the image
// chunk, not the configuration's color model.
func TestWebPExtendedLosslessIsChargedAsLossless(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "vp8x_lossless.webp"))
	require.NoError(t, err)
	cfg, err := decodeConfig(raw, mimeImageWebp)
	require.NoError(t, err)
	require.Equal(t, color.YCbCrModel, cfg.ColorModel, "fixture: VP8X without alpha reports YCbCr")
	decoded, err := webp.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	require.IsType(t, &image.NRGBA{}, decoded, "fixture: the image chunk is lossless")

	pixels := int64(cfg.Width) * int64(cfg.Height)
	floor := 8*pixels + resizeAndEncodeBytes(cfg.Width, cfg.Height) + fixedDecodeOverheadBytes
	require.GreaterOrEqual(t, estimateFor(t, raw, "vp8x_lossless.webp"), floor,
		"the NRGBA output and its transform copy must be charged")
}

// patchedLossyWebP returns testdata/lossy.webp with its RIFF and VP8 chunk
// lengths raised to declare chunkLen bytes of VP8 data that the file lacks.
func patchedLossyWebP(t *testing.T, chunkLen uint32) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "lossy.webp"))
	require.NoError(t, err)
	require.Equal(t, "VP8 ", string(raw[12:16]), "fixture: a simple lossy file")
	out := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(out[4:8], chunkLen+12)
	binary.LittleEndian.PutUint32(out[16:20], chunkLen)
	return out
}

// TestWebPRejectsTruncatedVP8Chunk covers the review's P2 on declared
// compressed buffers: golang.org/x/image/vp8 allocates the whole declared
// partition data (up to 128 MiB) before reading it, whatever the image
// dimensions. A VP8 chunk longer than the bytes present cannot decode, so it
// is refused before the decoder runs.
func TestWebPRejectsTruncatedVP8Chunk(t *testing.T) {
	raw := patchedLossyWebP(t, 16<<20-64)

	// Negative control: the decoder itself allocates the declared buffer.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := webp.Decode(bytes.NewReader(raw))
	runtime.ReadMemStats(&after)
	require.Error(t, err)
	require.Greater(t, after.TotalAlloc-before.TotalAlloc, uint64(12<<20), "fixture: the decoder must allocate the declared chunk")

	alloc, err := headerOnlyAlloc(t, raw, "truncated.webp")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrDecodeFailed), "got %v", err)
	require.Less(t, alloc, uint64(4<<20), "the declared VP8 buffer must not be allocated")

	// The real file still passes, and its compressed data is charged.
	valid, err := os.ReadFile(filepath.Join("testdata", "lossy.webp"))
	require.NoError(t, err)
	_, err = Normalize(context.Background(), valid, "lossy.webp")
	require.NoError(t, err)
}

// TestJPEGMarkerWalkFollowsDecoderOnStuffedBytes (second review, I1):
// image/jpeg skips a stray "\xff\x00" pair and any bytes up to the next 0xFF,
// but the estimator's walk read the pair as a marker with a length and jumped
// into a comment segment, where a fake one-component frame header made a
// progressive CMYK image look like a gray one.
func TestJPEGMarkerWalkFollowsDecoderOnStuffedBytes(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("testdata", "progressive_cmyk.jpg"))
	require.NoError(t, err)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(original))
	require.NoError(t, err)
	fakeSOF := []byte{0xFF, 0xC0, 0x00, 0x0B, 8, byte(cfg.Height >> 8), byte(cfg.Height), byte(cfg.Width >> 8), byte(cfg.Width), 1, 1, 0x11, 0}
	payload := append(make([]byte, 6), fakeSOF...)
	payload = append(payload, 0xFF, 0xDA, 0x00, 0x02)
	com := append([]byte{0xFF, 0xFE, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	crafted := append([]byte{0xFF, 0xD8, 0xFF, 0x00, 0x00, 0x0C}, com...)
	crafted = append(crafted, original[2:]...)

	decoded, err := jpeg.Decode(bytes.NewReader(crafted))
	require.NoError(t, err)
	require.IsType(t, &image.CMYK{}, decoded, "the decoder ignores the stray bytes and the comment (rules out a false positive)")
	require.GreaterOrEqual(t, estimateFor(t, crafted, "crafted.jpg"), estimateFor(t, original, "original.jpg"),
		"the crafted file must cost at least what the real image costs")
}

// TestWebPChargesFirstAlphaChunk (second review): the decoder decodes the
// first ALPH chunk and fails on a second one, so a later raw ALPH chunk must
// not replace the costlier classification of the first.
func TestWebPChargesFirstAlphaChunk(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "lossy_alpha.webp"))
	require.NoError(t, err)
	// testdata/lossy_alpha.webp: VP8X (10 bytes), ALPH (6409, odd: padded), VP8.
	const alphEnd = 12 + 18 + 8 + 6409 + 1
	require.Equal(t, "ALPH", string(raw[30:34]))
	extra := webpALPH(0)
	crafted := append(append(append([]byte(nil), raw[:alphEnd]...), extra...), raw[alphEnd:]...)
	binary.LittleEndian.PutUint32(crafted[4:8], uint32(len(crafted)-8))
	_, err = webp.Decode(bytes.NewReader(crafted))
	require.Error(t, err, "fixture: the decoder rejects the second ALPH chunk after decoding the first")
	require.Equal(t, estimateFor(t, raw, "lossy_alpha.webp"), estimateFor(t, crafted, "two_alph.webp"))
}
