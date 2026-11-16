package imageproc

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// noiseRGBA64 returns a w x h 16-bit image of random pixels, which PNG
// cannot compress: the largest output the pipeline can produce per pixel.
func noiseRGBA64(w, h int) *image.RGBA64 {
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test noise
	for i := 0; i < len(img.Pix); i += 8 {
		v := r.Uint64()
		for b := range 8 {
			img.Pix[i+b] = byte(v >> (8 * b))
		}
	}
	return img
}

// TestNormalizeReturnsExactSizePNG (maintainer review of PR #94, P2): the
// normalized PNG is held in memory from Prepare until the upload ends, for
// up to max_per_request images of every running create. It used to be the
// encoder's bytes.Buffer, whose capacity can reach twice its length, so a
// create retained up to 2x the bytes it would store.
func TestNormalizeReturnsExactSizePNG(t *testing.T) {
	for name, img := range map[string]image.Image{
		"small rgba":   patternImage("rgba", 300, 200),
		"noise 16-bit": noiseRGBA64(400, 300),
		"resized":      patternImage("rgba", 2000, 1000),
	} {
		var src bytes.Buffer
		require.NoError(t, png.Encode(&src, img))
		res, err := NewDecodeBudget(0).Normalize(context.Background(), src.Bytes(), "in.png")
		require.NoError(t, err, name)
		require.Equal(t, len(res.PNG), cap(res.PNG), "%s: the retained output must not carry encoder slack", name)
	}
}

// TestMaxNormalizedPNGBytesBoundsWorstCase pins the per-image term of the
// image memory envelope: 16-bit noise at the normalized edge (no resize, no
// compression possible) is the largest PNG Normalize can return.
func TestMaxNormalizedPNGBytesBoundsWorstCase(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, noiseRGBA64(NormalizedMaxEdge, NormalizedMaxEdge)))
	res, err := NewDecodeBudget(0).Normalize(context.Background(), src.Bytes(), "noise.png")
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(res.PNG)), MaxNormalizedPNGBytes)
	require.Greater(t, int64(len(res.PNG)), MaxNormalizedPNGBytes*99/100, "the bound must stay tight")

	// A larger 8-bit source is resized to RGBA, whose rows are half as wide.
	big := image.NewRGBA(image.Rect(0, 0, 3000, 3000))
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // deterministic test noise
	for i := range big.Pix {
		big.Pix[i] = byte(r.Uint32())
	}
	src.Reset()
	require.NoError(t, png.Encode(&src, big))
	res, err = NewDecodeBudget(0).Normalize(context.Background(), src.Bytes(), "big.png")
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(res.PNG)), MaxNormalizedPNGBytes/2+64<<10)
}
