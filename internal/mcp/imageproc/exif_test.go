package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyOrientationIdentity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 5))
	out := applyOrientation(img, 1)
	require.Same(t, img, out.(*image.RGBA))

	out2 := applyOrientation(img, 0)
	require.Same(t, img, out2.(*image.RGBA))
}

func TestApplyOrientationRotate90(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	out := applyOrientation(src, 6).(*image.RGBA)
	require.Equal(t, 2, out.Bounds().Dx())
	require.Equal(t, 3, out.Bounds().Dy())
}

func TestApplyOrientationFlip(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	out := applyOrientation(src, 2).(*image.RGBA)
	// Flipping horizontally puts the red pixel on the right.
	r, _, _, _ := out.At(1, 0).RGBA()
	require.NotZero(t, r)
}

func TestReadJPEGOrientationMissing(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 5))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	// Plain jpeg.Encode output does not carry EXIF, so orientation should be 1.
	require.Equal(t, 1, readJPEGOrientation(buf.Bytes()))
}

func TestReadJPEGOrientationWithTag(t *testing.T) {
	// Minimal JPEG: SOI + APP1(EXIF w/ orientation=6) + EOI.
	payload := buildJPEGWithOrientation(6)
	require.Equal(t, 6, readJPEGOrientation(payload))
}

// buildJPEGWithOrientation synthesizes a tiny JPEG whose only purpose is to
// carry an APP1 EXIF segment with a specific Orientation tag value.
func buildJPEGWithOrientation(orientation int) []byte {
	// APP1 segment body: "Exif\x00\x00" + TIFF header + 1 IFD entry.
	app1 := bytes.Buffer{}
	app1.WriteString("Exif\x00\x00")
	// TIFF little-endian header.
	app1.WriteString("II")
	app1.Write([]byte{0x2A, 0x00})
	// Offset to IFD0 = 8 bytes from TIFF start.
	app1.Write([]byte{0x08, 0x00, 0x00, 0x00})
	// Number of entries = 1.
	app1.Write([]byte{0x01, 0x00})
	// Entry: tag=0x0112, type=3 (SHORT), count=1, value=orientation, pad.
	app1.Write([]byte{0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00})
	app1.Write([]byte{byte(orientation), 0x00, 0x00, 0x00})
	// Next IFD offset = 0.
	app1.Write([]byte{0x00, 0x00, 0x00, 0x00})

	out := bytes.Buffer{}
	out.Write([]byte{0xFF, 0xD8}) // SOI
	out.Write([]byte{0xFF, 0xE1}) // APP1 marker
	segLen := uint16(app1.Len() + 2)
	out.WriteByte(byte(segLen >> 8))
	out.WriteByte(byte(segLen))
	out.Write(app1.Bytes())
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}
