package imageproc

import (
	"image"
	"image/color"
	"math"
	"math/bits"

	errors "github.com/Laisky/errors/v2"
)

// DefaultDecodeMemoryBudgetBytes is the default process-wide budget for the
// estimated transient heap of concurrent Normalize calls: 1.25 GiB. It admits
// every 8-bit-per-channel input of any supported format and layout up to the
// 8192 x 8192 contract limit (the worst such cases, a lossy WebP with
// VP8L-compressed alpha and a 20 MiB chunk and a single-strip RGBA TIFF, are
// estimated at ~1.24 and ~1.17 GiB) while bounding the aggregate decode memory of the
// shared API process. Only 16-bit interlaced PNG / 16-bit TIFF and progressive
// full-chroma or CMYK JPEG above roughly 40-60 MP (depending on layout) exceed it.
const DefaultDecodeMemoryBudgetBytes int64 = 1280 << 20

const (
	// resizeSampleBytes is what x/image/draw's kernel scaler keeps per
	// intermediate sample: a [4]float64 for every (destination column, source row).
	resizeSampleBytes = 32
	// jpegBlockBytes is the size of one 8x8 block of int32 coefficients that
	// image/jpeg keeps per component for progressive images.
	jpegBlockBytes = 64 * 4
	// fixedDecodeOverheadBytes covers decoder tables, bufio readers, zlib state
	// and other allocations that do not scale with the pixel count.
	fixedDecodeOverheadBytes = 1 << 20
	// worstCaseBytesPerPixel is used for color models the estimator does not know.
	worstCaseBytesPerPixel = 8
)

// estimateDecodeBytes returns a conservative upper bound of the transient heap
// that decoding raw, resizing it and re-encoding it as PNG will allocate. It
// only inspects headers, so it runs before any pixel buffer exists. cfg must
// come from the decoder that will decode raw (see decodeConfig).
func estimateDecodeBytes(raw []byte, mime string, cfg image.Config) (int64, error) {
	w, h := int64(cfg.Width), int64(cfg.Height)
	if w <= 0 || h <= 0 {
		return 0, errors.Wrapf(ErrDecodeFailed, "invalid image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	pixels := w * h

	var decoded int64
	switch mime {
	case mimeImageJpeg:
		decoded = estimateJPEGDecodeBytes(raw, cfg)
		if orientation := readJPEGOrientation(raw); orientation >= 2 && orientation <= 8 {
			// applyOrientation writes one oriented RGBA copy.
			decoded += 4 * pixels
		}
	case mimeImagePng:
		if err := checkPNGPixelData(raw, cfg); err != nil {
			return 0, errors.WithStack(err)
		}
		decoded = pixels * pngBytesPerPixel(raw, cfg)
		if pngInterlaced(raw) {
			// Each Adam7 pass is decoded into its own image before being merged.
			decoded *= 2
		}
	case mimeImageGif:
		// Only the first frame is decoded and it cannot exceed the logical
		// screen; an interlaced frame is copied once more when de-interlaced.
		decoded = 2 * pixels
	case mimeImageBmp:
		if err := checkBMPPixelData(raw, cfg); err != nil {
			return 0, errors.WithStack(err)
		}
		decoded = pixels * bytesPerPixel(cfg.ColorModel)
	case mimeImageTiff:
		decoded = satAdd(pixels*bytesPerPixel(cfg.ColorModel), tiffBlockBufferBytes(parseTIFFLayout(raw, cfg), int64(len(raw)), pixels))
	case mimeImageWebp:
		layout, err := scanWebP(raw)
		if err != nil {
			return 0, errors.WithStack(err)
		}
		decoded = estimateWebPDecodeBytes(layout, cfg)
	default:
		decoded = pixels * worstCaseBytesPerPixel
	}
	return satAdd(satAdd(decoded, resizeAndEncodeBytes(cfg.Width, cfg.Height)), fixedDecodeOverheadBytes), nil
}

// satMul returns a*b, or math.MaxInt64 when the product does not fit or an
// operand is negative. Estimates combine untrusted header fields (a TIFF tile
// edge is a 32-bit value), so they saturate instead of wrapping: a saturated
// estimate exceeds every budget and the input is refused before decoding.
func satMul(a, b int64) int64 {
	if a < 0 || b < 0 {
		return math.MaxInt64
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(lo)
}

// satAdd returns a+b, or math.MaxInt64 when the sum does not fit or an
// operand is negative (see satMul).
func satAdd(a, b int64) int64 {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// bytesPerPixel returns the in-memory size of one pixel for the image type a
// decoder returns for model.
func bytesPerPixel(model color.Model) int64 {
	switch model {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	}
	if _, ok := model.(color.Palette); ok {
		return 1
	}
	return worstCaseBytesPerPixel
}

// resizeAndEncodeBytes estimates the resize scratch, the resized image and the
// PNG encoder output for a w x h source, mirroring resizeIfNeeded.
func resizeAndEncodeBytes(w, h int) int64 {
	outW, outH := resizedDimensions(w, h, NormalizedMaxEdge)
	out := int64(outW) * int64(outH)
	// The encoder's growing output buffer allocates up to ~2x the encoded
	// size, and Normalize then copies the encoded bytes once (so it retains
	// no slack). A resized image is 8-bit RGBA, so 2 x 8 bytes per pixel
	// covers those 3 x 4; an unresized one may keep 16 bits per channel.
	if outW != w || outH != h {
		resize := int64(outW)*int64(h)*resizeSampleBytes + out*4
		return resize + 2*out*worstCaseBytesPerPixel
	}
	return 3 * out * worstCaseBytesPerPixel
}

// resizedDimensions returns the dimensions resizeIfNeeded produces.
func resizedDimensions(w, h, maxEdge int) (int, int) {
	longest := max(w, h)
	if longest <= maxEdge {
		return w, h
	}
	return max(w*maxEdge/longest, 1), max(h*maxEdge/longest, 1)
}

// deflateMaxRatio is the theoretical DEFLATE/zlib expansion limit: one
// length/distance pair needs at least two bits and yields at most 258 bytes.
// See docs/ref/image_decode_limits.md.
const deflateMaxRatio = 1032

// checkBMPPixelData rejects bitmaps whose file is too short to hold the pixel
// rows the header declares. golang.org/x/image/bmp only decodes uncompressed
// bitmaps and allocates the full image before reading any row, so a short
// header claiming 8192 x 8192 would otherwise allocate 256 MiB and then fail.
func checkBMPPixelData(raw []byte, cfg image.Config) error {
	const bppOffset = 14 + 14 // file header + offset of biBitCount in the info header
	if len(raw) < bppOffset+2 {
		return errors.Wrap(ErrDecodeFailed, "bmp header truncated")
	}
	bpp := int64(raw[bppOffset]) | int64(raw[bppOffset+1])<<8
	if bpp <= 0 {
		return errors.Wrap(ErrDecodeFailed, "bmp bit depth invalid")
	}
	stride := (int64(cfg.Width)*bpp + 31) / 32 * 4
	if need := stride * int64(cfg.Height); int64(len(raw)) < need {
		return errors.Wrapf(ErrDecodeFailed, "bmp pixel data truncated: %d bytes for %d expected", len(raw), need)
	}
	return nil
}
