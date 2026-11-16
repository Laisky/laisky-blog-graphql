package imageproc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// orientationSources returns every image type image/jpeg can return, with odd
// dimensions, plus a sub-image whose bounds do not start at the origin.
func orientationSources() map[string]image.Image {
	const w, h = 37, 23
	fill := func(set func(x, y int)) {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				set(x, y)
			}
		}
	}
	out := map[string]image.Image{}
	for name, ratio := range map[string]image.YCbCrSubsampleRatio{"ycbcr420": image.YCbCrSubsampleRatio420, "ycbcr444": image.YCbCrSubsampleRatio444} {
		img := image.NewYCbCr(image.Rect(0, 0, w, h), ratio)
		for i := range img.Y {
			img.Y[i] = uint8(i * 7)
		}
		for i := range img.Cb {
			img.Cb[i], img.Cr[i] = uint8(i*13), uint8(255-i*5)
		}
		out[name] = img
	}
	gray := image.NewGray(image.Rect(0, 0, w, h))
	fill(func(x, y int) { gray.SetGray(x, y, color.Gray{Y: uint8(x*9 + y)}) })
	out["gray"] = gray
	cmyk := image.NewCMYK(image.Rect(0, 0, w, h))
	fill(func(x, y int) {
		cmyk.SetCMYK(x, y, color.CMYK{C: uint8(x * 5), M: uint8(y * 11), Y: uint8(x + y), K: uint8(x * y)})
	})
	out["cmyk"] = cmyk
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(func(x, y int) { rgba.SetRGBA(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 7), B: 90, A: 255}) })
	out["rgba"] = rgba
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	fill(func(x, y int) {
		nrgba.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 7), B: 90, A: uint8(x * 6)})
	})
	out["nrgba"] = nrgba
	out["ycbcr-subimage"] = out["ycbcr420"].(*image.YCbCr).SubImage(image.Rect(3, 2, 30, 21))
	return out
}

// TestApplyOrientationMatchesLegacy proves the single-copy orientation is
// byte-identical to the previous implementation for every orientation value
// and every source type, so stored PNGs (and their SHA-256 dedup keys) of
// oriented JPEGs do not change.
func TestApplyOrientationMatchesLegacy(t *testing.T) {
	for name, src := range orientationSources() {
		for orientation := 0; orientation <= 9; orientation++ {
			want := legacyApplyOrientation(src, orientation)
			got := applyOrientation(src, orientation)
			require.Equal(t, want.Bounds().Size(), got.Bounds().Size(), "%s orientation %d", name, orientation)
			wantRGBA, wantOK := want.(*image.RGBA)
			gotRGBA, gotOK := got.(*image.RGBA)
			if orientation < 2 || orientation > 8 {
				require.Same(t, src, got, "%s orientation %d is a no-op", name, orientation)
				continue
			}
			require.True(t, wantOK && gotOK, "%s orientation %d", name, orientation)
			require.Equal(t, wantRGBA.Pix, gotRGBA.Pix, "%s orientation %d", name, orientation)
		}
	}
}

// TestApplyOrientationAllocatesOneCopy pins the orientation working set to a
// single RGBA image; the previous code made up to three full copies and
// boxed one color per pixel (issue #83 review: 8192^2 orientation 3 peaked
// at ~1 GiB).
func TestApplyOrientationAllocatesOneCopy(t *testing.T) {
	const edge = 1024
	src := image.NewYCbCr(image.Rect(0, 0, edge, edge), image.YCbCrSubsampleRatio420)
	limit := uint64(4*edge*edge + 64<<10)
	for _, orientation := range []int{2, 3, 4, 5, 6, 7, 8} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_ = applyOrientation(src, orientation)
		runtime.ReadMemStats(&after)
		require.LessOrEqual(t, after.TotalAlloc-before.TotalAlloc, limit, "orientation %d", orientation)
	}
}

// exifAPP1 builds a JPEG APP1 segment whose IFD declares entryCount entries
// and carries the given raw entry bytes.
func exifAPP1(entryCount uint16, entries []byte) []byte {
	body := []byte("Exif\x00\x00II\x2A\x00\x08\x00\x00\x00")
	body = append(body, byte(entryCount), byte(entryCount>>8))
	body = append(body, entries...)
	segLen := len(body) + 2
	return append([]byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}, body...)
}

// TestReadJPEGOrientationHostileIFD covers the pre-existing panic found in
// review: an IFD entry count larger than the segment made parseAPP1 slice
// past the buffer ("slice bounds out of range").
func TestReadJPEGOrientationHostileIFD(t *testing.T) {
	orientation6 := []byte{0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00}
	cases := map[string]struct {
		segment []byte
		want    int
	}{
		"count far beyond data":         {exifAPP1(0xFFFF, nil), 1},
		"count one beyond data":         {exifAPP1(2, orientation6), 6},
		"orientation after short entry": {exifAPP1(1, orientation6[:11]), 1},
		"valid":                         {exifAPP1(1, orientation6), 6},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := append([]byte{0xFF, 0xD8}, tc.segment...)
			raw = append(raw, 0xFF, 0xDA, 0x00, 0x02)
			require.NotPanics(t, func() { require.Equal(t, tc.want, readJPEGOrientation(raw)) })
		})
	}

	// End to end through Normalize, with a real image after the segment.
	base := encodeFixture(t, "jpeg", patternImage("rgba", 16, 16))
	raw := append([]byte{0xFF, 0xD8}, exifAPP1(0xFFFF, nil)...)
	raw = append(raw, base[2:]...)
	require.NotPanics(t, func() { _, _ = Normalize(context.Background(), raw, "x.jpg") })
}

// FuzzReadJPEGOrientation checks the EXIF parser never panics and only
// reports values it read from a SHORT Orientation entry (or the default 1).
func FuzzReadJPEGOrientation(f *testing.F) {
	orientation6 := []byte{0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00}
	f.Add(append([]byte{0xFF, 0xD8}, exifAPP1(1, orientation6)...))
	f.Add(append([]byte{0xFF, 0xD8}, exifAPP1(0xFFFF, nil)...))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x02})
	f.Add([]byte("\xFF\xD8\xFF\xE1\x00\x10Exif\x00\x00MM\x00\x2A\xFF\xFF\xFF\xF0"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		got := readJPEGOrientation(raw)
		require.GreaterOrEqual(t, got, 0)
		require.LessOrEqual(t, got, 0xFFFF)
		if got >= 2 && got <= 8 && bytes.HasPrefix(raw, []byte{0xFF, 0xD8}) {
			// Orientation must be applicable without panicking.
			src := image.NewRGBA(image.Rect(0, 0, 3, 2))
			draw.Draw(src, src.Bounds(), image.NewUniform(color.RGBA{R: 1, A: 255}), image.Point{}, draw.Src)
			require.NotPanics(t, func() { _ = applyOrientation(src, got) })
		}
	})
}
