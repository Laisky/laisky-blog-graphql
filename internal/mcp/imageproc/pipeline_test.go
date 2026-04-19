package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// makePNG builds a solid-color PNG of the given size.
func makePNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}))
	return buf.Bytes()
}

func makeBMP(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, bmp.Encode(&buf, img))
	return buf.Bytes()
}

func makeTIFF(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, tiff.Encode(&buf, img, nil))
	return buf.Bytes()
}

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	img := image.NewPaletted(image.Rect(0, 0, w, h), palette)
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, &gif.Options{NumColors: 2}))
	return buf.Bytes()
}

func TestNormalizePNG(t *testing.T) {
	raw := makePNG(t, 400, 300, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	res, err := Normalize(raw, "a.png")
	require.NoError(t, err)
	require.Equal(t, "image/png", res.OriginalMIME)
	require.Equal(t, 400, res.Width)
	require.Equal(t, 300, res.Height)
	require.NotEmpty(t, res.SHA256)
	require.Greater(t, len(res.PNG), 0)

	// Re-running produces the same SHA256 (deterministic).
	res2, err := Normalize(raw, "a.png")
	require.NoError(t, err)
	require.Equal(t, res.SHA256, res2.SHA256)
}

func TestNormalizeJPEGResized(t *testing.T) {
	raw := makeJPEG(t, 3000, 2000, color.RGBA{R: 240, G: 200, B: 80, A: 255})
	res, err := Normalize(raw, "a.jpg")
	require.NoError(t, err)
	require.LessOrEqual(t, res.Width, NormalizedMaxEdge)
	require.LessOrEqual(t, res.Height, NormalizedMaxEdge)
	require.Equal(t, "image/jpeg", res.OriginalMIME)
}

func TestNormalizeGIFExtractsFrame(t *testing.T) {
	raw := makeGIF(t, 100, 60)
	res, err := Normalize(raw, "a.gif")
	require.NoError(t, err)
	require.Equal(t, "image/gif", res.OriginalMIME)
	require.Equal(t, 100, res.Width)
	require.Equal(t, 60, res.Height)
}

func TestNormalizeBMP(t *testing.T) {
	raw := makeBMP(t, 80, 40, color.RGBA{R: 50, G: 50, B: 200, A: 255})
	res, err := Normalize(raw, "a.bmp")
	require.NoError(t, err)
	require.Equal(t, "image/bmp", res.OriginalMIME)
}

func TestNormalizeTIFF(t *testing.T) {
	raw := makeTIFF(t, 80, 40, color.RGBA{R: 70, G: 180, B: 90, A: 255})
	res, err := Normalize(raw, "a.tiff")
	require.NoError(t, err)
	require.Equal(t, "image/tiff", res.OriginalMIME)
}

func TestNormalizeSVGRejected(t *testing.T) {
	raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="10"/></svg>`)
	_, err := Normalize(raw, "x.svg")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrUnsupportedMIME) || errs.Is(err, ErrDecodeFailed))
}

func TestNormalizeHEICRejected(t *testing.T) {
	// Fake HEIC-like bytes with a valid ftyp header; magic-byte sniff should
	// refuse because it is not one of the allowed input MIME types.
	raw := append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypheic")...)
	raw = append(raw, make([]byte, 64)...)
	_, err := Normalize(raw, "x.heic")
	require.Error(t, err)
}

func TestNormalizeCorruptJPEG(t *testing.T) {
	raw := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0}
	_, err := Normalize(raw, "bad.jpg")
	require.Error(t, err)
}

func TestNormalizeRejectsTooLargeDimensions(t *testing.T) {
	// Generate a PNG whose dimensions exceed the decode-bomb guard. We build a
	// minimal header by hand to avoid allocating a real pixel buffer.
	header := makeLargePNGHeader(9000, 9000)
	_, err := Normalize(header, "big.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrDimensionsTooLarge) || errs.Is(err, ErrDecodeFailed))
}

// makeLargePNGHeader synthesizes just enough PNG bytes that DecodeConfig
// succeeds but the pixel buffer is absent. The resulting payload should fail
// decode-bomb validation before allocation.
func makeLargePNGHeader(w, h int) []byte {
	// Build a real PNG at small size then patch width/height in IHDR chunk.
	small := makePNGBytes(1, 1)
	// IHDR starts at offset 8 (signature) + 8 (chunk length + type).
	ihdrStart := 8 + 4 + 4
	be := func(v int) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	copy(small[ihdrStart:ihdrStart+4], be(w))
	copy(small[ihdrStart+4:ihdrStart+8], be(h))
	return small
}

func makePNGBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
