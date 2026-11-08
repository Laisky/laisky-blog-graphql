package imageproc

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// patternImage fills a w x h image of the requested type with a cheap gradient.
func patternImage(kind string, w, h int) image.Image {
	rect := image.Rect(0, 0, w, h)
	switch kind {
	case "gray":
		img := image.NewGray(rect)
		for i := range img.Pix {
			img.Pix[i] = uint8(i / 97)
		}
		return img
	case "rgba64":
		img := image.NewNRGBA64(rect)
		for i := range img.Pix {
			img.Pix[i] = uint8(i / 101)
		}
		return img
	case "paletted":
		img := image.NewPaletted(rect, color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}})
		for i := range img.Pix {
			img.Pix[i] = uint8((i / 89) % 3)
		}
		return img
	default:
		img := image.NewRGBA(rect)
		for i := range img.Pix {
			img.Pix[i] = uint8(i / 103)
		}
		return img
	}
}

// encodeFixture encodes img with the named codec.
func encodeFixture(t *testing.T, codec string, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch codec {
	case "png":
		require.NoError(t, png.Encode(&buf, img))
	case "jpeg":
		require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 40}))
	case "gif":
		require.NoError(t, gif.Encode(&buf, img, &gif.Options{NumColors: 4}))
	case "bmp":
		require.NoError(t, bmp.Encode(&buf, img))
	case "tiff-deflate":
		require.NoError(t, tiff.Encode(&buf, img, &tiff.Options{Compression: tiff.Deflate}))
	case "tiff":
		require.NoError(t, tiff.Encode(&buf, img, nil))
	default:
		t.Fatalf("unknown codec %q", codec)
	}
	return buf.Bytes()
}

// minNormalizeAlloc reports the smallest heap allocation of two Normalize runs.
func minNormalizeAlloc(t *testing.T, budget *DecodeBudget, raw []byte, name string) uint64 {
	t.Helper()
	var best uint64
	for i := range 2 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := budget.Normalize(context.Background(), raw, name)
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		if delta := after.TotalAlloc - before.TotalAlloc; i == 0 || delta < best {
			best = delta
		}
	}
	return best
}

// TestEstimateDecodeBytesBoundsActualAllocation checks the admission estimate
// against what the real decoders, the resizer and the encoder allocate, for
// every supported format and the layouts with the largest decoder scratch
// (progressive and CMYK JPEG, interlaced PNG, alpha and lossless WebP).
func TestEstimateDecodeBytesBoundsActualAllocation(t *testing.T) {
	if raceEnabled {
		t.Skip("heap totals are inflated by the race detector's sync.Pool behavior; covered by the non-race test run")
	}
	type fixture struct {
		name string
		raw  []byte
	}
	fixtures := []fixture{
		{"rgba.png", encodeFixture(t, "png", patternImage("rgba", 2400, 1600))},
		{"rgba64.png", encodeFixture(t, "png", patternImage("rgba64", 2000, 1300))},
		{"gray.png", encodeFixture(t, "png", patternImage("gray", 1800, 1800))},
		{"paletted.png", encodeFixture(t, "png", patternImage("paletted", 1700, 900))},
		{"small.png", encodeFixture(t, "png", patternImage("rgba", 320, 200))},
		{"baseline.jpg", encodeFixture(t, "jpeg", patternImage("rgba", 2400, 1600))},
		{"gray.jpg", encodeFixture(t, "jpeg", patternImage("gray", 1800, 1200))},
		{"frame.gif", encodeFixture(t, "gif", patternImage("paletted", 2000, 1500))},
		{"rgb.bmp", encodeFixture(t, "bmp", patternImage("rgba", 1800, 1200))},
		{"paletted.bmp", encodeFixture(t, "bmp", patternImage("paletted", 1800, 1200))},
		{"deflate.tiff", encodeFixture(t, "tiff-deflate", patternImage("rgba", 1800, 1200))},
		{"rgba64.tiff", encodeFixture(t, "tiff", patternImage("rgba64", 1600, 1000))},
	}
	for _, name := range []string{
		"progressive_444.jpg", "progressive_420.jpg", "progressive_cmyk.jpg", "progressive_gray.jpg",
		"interlaced_rgba.png", "lossless.webp", "lossy.webp", "lossy_alpha.webp",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		fixtures = append(fixtures, fixture{name, raw})
	}

	budget := NewDecodeBudget(DefaultDecodeMemoryBudgetBytes)
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			mime, err := detectMIME(fx.raw, fx.name)
			require.NoError(t, err)
			cfg, err := decodeConfig(fx.raw, mime)
			require.NoError(t, err)
			estimate, err := estimateDecodeBytes(fx.raw, mime, cfg)
			require.NoError(t, err)

			actual := minNormalizeAlloc(t, budget, fx.raw, fx.name)
			t.Logf("%s %dx%d %T: estimate=%d actual=%d ratio=%.2f", mime, cfg.Width, cfg.Height, cfg.ColorModel, estimate, actual, float64(estimate)/float64(actual))
			require.LessOrEqual(t, actual, uint64(estimate), "estimate must bound the real allocation")
		})
	}
}

// pngHeader returns a PNG signature plus a CRC-valid IHDR chunk and an empty
// IDAT, enough for png.DecodeConfig and the estimator without pixel data.
func pngHeader(w, h int, bitDepth, colorType, interlace byte) []byte {
	chunk := func(kind string, data []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		out = append(out, kind...)
		out = append(out, data...)
		return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, bitDepth, colorType, 0, 0, interlace)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", ihdr)...)
	return append(out, chunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
}

// jpegHeader returns SOI, an optional Adobe APP14 segment, and a start-of-frame
// segment with the given components ({id, sampling}) followed by SOS.
func jpegHeader(marker byte, w, h int, adobe bool, comps ...[2]byte) []byte {
	out := []byte{0xFF, 0xD8}
	if adobe {
		out = append(out, 0xFF, 0xEE, 0x00, 0x0E, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0)
	}
	seg := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(len(comps))}
	for _, c := range comps {
		seg = append(seg, c[0], c[1], 0)
	}
	out = append(out, 0xFF, marker, byte((len(seg)+2)>>8), byte(len(seg)+2))
	out = append(out, seg...)
	return append(out, 0xFF, 0xDA, 0x00, 0x02)
}

// TestDecodeBudgetDefaultCoversContract pins which inputs at the documented
// 8192 x 8192 limit the default budget admits: every 8-bit-per-channel layout
// of every format fits (as do non-interlaced 16-bit PNG and progressive 4:2:0
// JPEG); only the combinations documented in the proposal exceed it.
func TestDecodeBudgetDefaultCoversContract(t *testing.T) {
	const edge = MaxDecodeDimension
	ycc := func(sampling byte) [][2]byte { return [][2]byte{{1, sampling}, {2, 0x11}, {3, 0x11}} }
	cmyk := [][2]byte{{'C', 0x11}, {'M', 0x11}, {'Y', 0x11}, {'K', 0x11}}
	full := image.Config{Width: edge, Height: edge}
	with := func(model color.Model) image.Config {
		cfg := full
		cfg.ColorModel = model
		return cfg
	}

	cases := []struct {
		name  string
		raw   []byte
		mime  string
		cfg   image.Config
		admit bool
	}{
		{"png rgba", pngHeader(edge, edge, 8, 6, 0), mimeImagePng, with(color.NRGBAModel), true},
		{"png rgba interlaced", pngHeader(edge, edge, 8, 6, 1), mimeImagePng, with(color.NRGBAModel), true},
		{"png rgba64", pngHeader(edge, edge, 16, 6, 0), mimeImagePng, with(color.NRGBA64Model), true},
		{"gif", nil, mimeImageGif, with(color.Palette{color.Black}), true},
		{"tiff rgba", nil, mimeImageTiff, with(color.RGBAModel), true},
		{"jpeg baseline 4:4:4", jpegHeader(0xC0, edge, edge, false, ycc(0x11)...), mimeImageJpeg, with(color.YCbCrModel), true},
		{"jpeg baseline cmyk", jpegHeader(0xC0, edge, edge, true, cmyk...), mimeImageJpeg, with(color.CMYKModel), true},
		{"jpeg progressive 4:2:0", jpegHeader(0xC2, edge, edge, false, ycc(0x22)...), mimeImageJpeg, with(color.YCbCrModel), true},
		{"webp lossy", nil, mimeImageWebp, with(color.YCbCrModel), true},
		{"webp lossless", nil, mimeImageWebp, with(color.NRGBAModel), true},
		{"webp lossy alpha", nil, mimeImageWebp, with(color.NYCbCrAModel), true},
		{"png rgba64 interlaced", pngHeader(edge, edge, 16, 6, 1), mimeImagePng, with(color.NRGBA64Model), false},
		{"tiff rgba64", nil, mimeImageTiff, with(color.RGBA64Model), false},
		{"jpeg progressive 4:4:4", jpegHeader(0xC2, edge, edge, false, ycc(0x11)...), mimeImageJpeg, with(color.YCbCrModel), false},
		{"jpeg progressive cmyk", jpegHeader(0xC2, edge, edge, true, cmyk...), mimeImageJpeg, with(color.CMYKModel), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if tc.mime == mimeImagePng {
				// Satisfy the inflate-ratio plausibility check without real pixels.
				raw = append(raw, make([]byte, 600<<10)...)
			}
			estimate, err := estimateDecodeBytes(raw, tc.mime, tc.cfg)
			require.NoError(t, err)
			t.Logf("estimate=%d MiB", estimate>>20)
			if tc.admit {
				require.LessOrEqual(t, estimate, DefaultDecodeMemoryBudgetBytes)
			} else {
				require.Greater(t, estimate, DefaultDecodeMemoryBudgetBytes)
			}
		})
	}
}

// TestDecodeBudgetRejectsBeforeDecoding proves an over-budget image is
// refused from its header alone, without allocating its pixel buffer.
func TestDecodeBudgetRejectsBeforeDecoding(t *testing.T) {
	raw := encodeFixture(t, "png", patternImage("rgba", 1200, 1200))
	budget := NewDecodeBudget(4 << 20)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := budget.Normalize(context.Background(), raw, "big.png")
	runtime.ReadMemStats(&after)

	require.Error(t, err)
	require.True(t, errs.Is(err, ErrDimensionsTooLarge), "got %v", err)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(2<<20), "the 5.5 MiB pixel buffer must not be allocated")
}

// TestDecodeBudgetQueuesUntilCapacityIsReleased proves the budget bounds
// concurrent decodes: while it is exhausted Normalize waits and gives up with
// ErrDecodeBusy when its context ends; once released, decoding proceeds.
func TestDecodeBudgetQueuesUntilCapacityIsReleased(t *testing.T) {
	raw := encodeFixture(t, "png", patternImage("rgba", 200, 100))
	budget := NewDecodeBudget(64 << 20)

	release, err := budget.acquire(context.Background(), budget.Capacity())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = budget.Normalize(ctx, raw, "queued.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrDecodeBusy), "got %v", err)

	done := make(chan error, 1)
	go func() {
		_, normalizeErr := budget.Normalize(context.Background(), raw, "queued.png")
		done <- normalizeErr
	}()
	select {
	case err := <-done:
		t.Fatalf("Normalize must wait for the exhausted budget, returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Normalize did not proceed after the budget was released")
	}

	// A canceled context is refused even when capacity is free.
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = budget.Normalize(canceled, raw, "queued.png")
	require.True(t, errs.Is(err, ErrDecodeBusy), "got %v", err)
}

// headerOnlyAlloc measures the allocation of a Normalize call that must fail.
func headerOnlyAlloc(t *testing.T, raw []byte, name string) (uint64, error) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := Normalize(context.Background(), raw, name)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, err
}

// TestNormalizeRejectsHeaderOnlyBombsWithoutAllocating covers truncated
// uncompressed bitmaps and PNGs that declare the maximum dimensions: their
// decoders allocate the full image (256-512 MiB) before discovering the
// missing pixel data, so the plausibility checks must refuse them first.
func TestNormalizeRejectsHeaderOnlyBombsWithoutAllocating(t *testing.T) {
	t.Run("bmp", func(t *testing.T) {
		raw := encodeFixture(t, "bmp", patternImage("rgba", 2, 2))
		patched := append([]byte(nil), raw[:54]...)
		binary.LittleEndian.PutUint32(patched[18:22], MaxDecodeDimension)
		binary.LittleEndian.PutUint32(patched[22:26], MaxDecodeDimension)
		alloc, err := headerOnlyAlloc(t, patched, "bomb.bmp")
		require.Error(t, err)
		require.True(t, errs.Is(err, ErrDecodeFailed), "got %v", err)
		require.Less(t, alloc, uint64(4<<20), "no 8192x8192 buffer may be allocated")
	})

	t.Run("png", func(t *testing.T) {
		raw := pngHeader(MaxDecodeDimension, MaxDecodeDimension, 16, 6, 0)
		alloc, err := headerOnlyAlloc(t, raw, "bomb.png")
		require.Error(t, err)
		require.True(t, errs.Is(err, ErrDecodeFailed), "got %v", err)
		require.Less(t, alloc, uint64(4<<20), "no 8192x8192 buffer may be allocated")
	})

	t.Run("legit images pass the plausibility checks", func(t *testing.T) {
		for _, codec := range []string{"png", "bmp"} {
			for _, kind := range []string{"rgba", "paletted", "gray"} {
				raw := encodeFixture(t, codec, patternImage(kind, 160, 120))
				_, err := Normalize(context.Background(), raw, "ok."+codec)
				require.NoError(t, err, "%s %s", codec, kind)
			}
		}
		// A wide uniform PNG compresses close to the DEFLATE limit (~885:1
		// with image/png) and must still pass.
		uniform := image.NewNRGBA64(image.Rect(0, 0, MaxDecodeDimension, 128))
		raw := encodeFixture(t, "png", uniform)
		require.Greater(t, int64(MaxDecodeDimension)*8*128/int64(len(raw)), int64(500), "fixture must be highly compressed")
		_, err := Normalize(context.Background(), raw, "uniform.png")
		require.NoError(t, err)
	})
}

// TestNormalizeRejectsDimensionsOverContract pins the 8192 px per-axis guard
// with CRC-valid headers, so the rejection cannot come from a checksum error.
func TestNormalizeRejectsDimensionsOverContract(t *testing.T) {
	for _, size := range [][2]int{{MaxDecodeDimension + 1, 1}, {1, MaxDecodeDimension + 1}} {
		raw := pngHeader(size[0], size[1], 8, 6, 0)
		_, err := Normalize(context.Background(), raw, "wide.png")
		require.Error(t, err)
		require.True(t, errs.Is(err, ErrDimensionsTooLarge), "%v: got %v", size, err)
	}
	// The limit itself is accepted.
	edge := encodeFixture(t, "png", patternImage("gray", MaxDecodeDimension, 2))
	res, err := Normalize(context.Background(), edge, "edge.png")
	require.NoError(t, err)
	require.Equal(t, NormalizedMaxEdge, res.Width)
}
