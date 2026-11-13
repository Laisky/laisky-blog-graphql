package imageproc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"runtime"
	"testing"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// gifFrame builds a paletted frame whose pixels follow a deterministic pattern
// seeded by variant, so different variants produce visibly different frames.
func gifFrame(bounds image.Rectangle, palette color.Palette, variant int) *image.Paletted {
	frame := image.NewPaletted(bounds, palette)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			frame.SetColorIndex(x, y, uint8((x/7+y/5+variant)%len(palette)))
		}
	}
	return frame
}

// encodeAnimatedGIF encodes frames as one animated GIF with a w x h logical screen.
func encodeAnimatedGIF(t *testing.T, w, h int, frames ...*image.Paletted) []byte {
	t.Helper()
	g := &gif.GIF{Config: image.Config{Width: w, Height: h}}
	for _, frame := range frames {
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 1)
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, g))
	return buf.Bytes()
}

// legacyGIFFirstFramePNG reproduces the pre-fix pipeline byte for byte:
// gif.DecodeAll, keep Image[0], resize, re-encode. It is the reference that
// proves the first-frame-only decoder did not change the stored output.
func legacyGIFFirstFramePNG(t *testing.T, raw []byte) []byte {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NotEmpty(t, g.Image)
	resized := resizeIfNeeded(g.Image[0], NormalizedMaxEdge)
	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.DefaultCompression}
	require.NoError(t, encoder.Encode(&buf, resized))
	return buf.Bytes()
}

// stripGIFTrailer removes the single-byte GIF trailer (0x3B) so a test can
// append further blocks after the frames an encoder produced.
func stripGIFTrailer(t *testing.T, raw []byte) []byte {
	t.Helper()
	require.Equal(t, byte(0x3B), raw[len(raw)-1], "encoder output must end with the GIF trailer")
	return append([]byte(nil), raw[:len(raw)-1]...)
}

// TestNormalizeGIFIgnoresBlocksAfterFirstFrame proves Normalize stops parsing
// once the first frame is decoded: a valid first frame followed by a second
// image descriptor whose bounds exceed the logical screen (and no trailer)
// must still normalize, and to exactly the bytes of the first frame alone.
// gif.DecodeAll rejects this input because it parses and allocates every frame.
func TestNormalizeGIFIgnoresBlocksAfterFirstFrame(t *testing.T) {
	palette := color.Palette{color.Black, color.White, color.RGBA{R: 200, A: 255}, color.RGBA{B: 200, A: 255}}
	first := gifFrame(image.Rect(0, 0, 64, 48), palette, 0)
	single := encodeAnimatedGIF(t, 64, 48, first)

	malformed := stripGIFTrailer(t, single)
	// Image descriptor: separator, left, top, width=0xFFFF, height=0xFFFF, no local table.
	malformed = append(malformed, 0x2C, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0x00)
	malformed = append(malformed, 0x02, 0x02, 0x4C, 0x01, 0x00)

	_, legacyErr := gif.DecodeAll(bytes.NewReader(malformed))
	require.Error(t, legacyErr, "fixture must be rejected by a decoder that parses every frame")

	got, err := Normalize(context.Background(), malformed, "trailing.gif")
	require.NoError(t, err)
	want, err := Normalize(context.Background(), single, "single.gif")
	require.NoError(t, err)
	require.Equal(t, want.PNG, got.PNG)
	require.Equal(t, "image/gif", got.OriginalMIME)
	require.Equal(t, 64, got.Width)
	require.Equal(t, 48, got.Height)
}

// measureNormalizeAlloc returns the smallest heap allocation total observed
// across a few Normalize runs, which damps GC and background-goroutine noise.
func measureNormalizeAlloc(t *testing.T, raw []byte) (PipelineResult, uint64) {
	t.Helper()
	var (
		best   uint64
		result PipelineResult
	)
	for i := range 3 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		res, err := Normalize(context.Background(), raw, "fixture.gif")
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		delta := after.TotalAlloc - before.TotalAlloc
		if i == 0 || delta < best {
			best = delta
		}
		result = res
	}
	return result, best
}

// TestNormalizeGIFAllocationIndependentOfFrameCount reproduces issue #83: a
// many-frame GIF must not allocate per discarded frame. Each 1024x1024 frame
// costs 1 MiB when decoded, so decoding all 24 frames would allocate at least
// 23 MiB more than the single-frame fixture; first-frame decoding stays within
// a few MiB of it regardless of the frame count.
func TestNormalizeGIFAllocationIndependentOfFrameCount(t *testing.T) {
	const (
		edge       = 1024
		manyFrames = 24
		slack      = 4 << 20
	)
	palette := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, edge, edge), palette)
	frames := make([]*image.Paletted, manyFrames)
	for i := range frames {
		frames[i] = frame
	}
	one := encodeAnimatedGIF(t, edge, edge, frame)
	many := encodeAnimatedGIF(t, edge, edge, frames...)
	require.Less(t, len(many), 1<<20, "fixture stays tiny on the wire")

	oneRes, oneAlloc := measureNormalizeAlloc(t, one)
	manyRes, manyAlloc := measureNormalizeAlloc(t, many)
	require.Equal(t, oneRes.PNG, manyRes.PNG, "both fixtures share the same first frame")
	t.Logf("1 frame: compressed=%d allocated=%d; %d frames: compressed=%d allocated=%d", len(one), oneAlloc, manyFrames, len(many), manyAlloc)
	require.Less(t, manyAlloc, oneAlloc+slack, "allocation must not grow with discarded frames")
}

// TestNormalizeAnimatedGIFMatchesLegacyFirstFrame pins the first-frame output
// to the pre-fix pipeline byte for byte, including a first frame that is
// offset inside the logical screen, uses a local palette, and has a
// transparent index, followed by a different second frame.
func TestNormalizeAnimatedGIFMatchesLegacyFirstFrame(t *testing.T) {
	t.Run("full-screen frames", func(t *testing.T) {
		palette := color.Palette{color.Black, color.White, color.RGBA{G: 180, A: 255}}
		raw := encodeAnimatedGIF(t, 120, 90,
			gifFrame(image.Rect(0, 0, 120, 90), palette, 0),
			gifFrame(image.Rect(0, 0, 120, 90), palette, 1),
		)
		res, err := Normalize(context.Background(), raw, "anim.gif")
		require.NoError(t, err)
		require.Equal(t, legacyGIFFirstFramePNG(t, raw), res.PNG)
		require.Equal(t, 120, res.Width)
		require.Equal(t, 90, res.Height)
	})

	t.Run("offset frame with transparency", func(t *testing.T) {
		local := color.Palette{color.RGBA{}, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
		first := gifFrame(image.Rect(10, 6, 70, 40), local, 0)
		second := gifFrame(image.Rect(0, 0, 80, 50), color.Palette{color.Black, color.White}, 3)
		g := &gif.GIF{
			Image:    []*image.Paletted{first, second},
			Delay:    []int{5, 5},
			Disposal: []byte{gif.DisposalBackground, gif.DisposalNone},
			Config:   image.Config{Width: 80, Height: 50},
		}
		var buf bytes.Buffer
		require.NoError(t, gif.EncodeAll(&buf, g))
		raw := buf.Bytes()

		res, err := Normalize(context.Background(), raw, "offset.gif")
		require.NoError(t, err)
		require.Equal(t, legacyGIFFirstFramePNG(t, raw), res.PNG)
	})

	t.Run("large frames are resized identically", func(t *testing.T) {
		palette := color.Palette{color.Black, color.White, color.RGBA{R: 90, G: 90, B: 255, A: 255}}
		raw := encodeAnimatedGIF(t, 2000, 400,
			gifFrame(image.Rect(0, 0, 2000, 400), palette, 2),
			gifFrame(image.Rect(0, 0, 2000, 400), palette, 0),
		)
		res, err := Normalize(context.Background(), raw, "wide.gif")
		require.NoError(t, err)
		require.Equal(t, legacyGIFFirstFramePNG(t, raw), res.PNG)
		require.Equal(t, NormalizedMaxEdge, res.Width)
	})
}

// TestNormalizeStaticGIFMatchesLegacy keeps the single-frame path unchanged.
func TestNormalizeStaticGIFMatchesLegacy(t *testing.T) {
	raw := makeGIF(t, 100, 60)
	res, err := Normalize(context.Background(), raw, "static.gif")
	require.NoError(t, err)
	require.Equal(t, legacyGIFFirstFramePNG(t, raw), res.PNG)
}

// TestNormalizeGIFMalformedFirstFrame keeps first-frame validation strict.
func TestNormalizeGIFMalformedFirstFrame(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	valid := encodeAnimatedGIF(t, 32, 32, gifFrame(image.Rect(0, 0, 32, 32), palette, 0))

	cases := map[string][]byte{
		// Header and screen descriptor only: no frame at all.
		"no frames": append(append([]byte(nil), valid[:13+6]...), 0x3B),
		// The first frame's LZW stream is cut off mid-way.
		"truncated pixel data": valid[:len(valid)-8],
	}
	// First image descriptor claims bounds outside the 32x32 logical screen.
	outOfBounds := append([]byte(nil), valid[:13+6]...)
	outOfBounds = append(outOfBounds, 0x2C, 0, 0, 0, 0, 64, 0, 64, 0, 0x00, 0x02, 0x02, 0x4C, 0x01, 0x00, 0x3B)
	cases["frame outside screen"] = outOfBounds

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Normalize(context.Background(), raw, "bad.gif")
			require.Error(t, err)
			require.True(t, errs.Is(err, ErrDecodeFailed), "got %v", err)
		})
	}
}

// TestNormalizeGIFRejectsOversizedLogicalScreen keeps the 8192 px guard for
// GIF, whose DecodeConfig reports the logical screen size.
func TestNormalizeGIFRejectsOversizedLogicalScreen(t *testing.T) {
	raw := makeGIF(t, 4, 4)
	patched := append([]byte(nil), raw...)
	// Logical screen width is the little-endian uint16 at offset 6.
	patched[6], patched[7] = byte((MaxDecodeDimension+1)&0xFF), byte((MaxDecodeDimension+1)>>8)
	_, err := Normalize(context.Background(), patched, "wide.gif")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrDimensionsTooLarge), "got %v", err)
}
