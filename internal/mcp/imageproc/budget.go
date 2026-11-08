package imageproc

import (
	"context"
	"image"
	"image/color"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/sync/semaphore"
)

// DefaultDecodeMemoryBudgetBytes is the default process-wide budget for the
// estimated transient heap of concurrent Normalize calls: 1.25 GiB. It admits
// every 8-bit-per-channel input of any supported format and layout up to the
// 8192 x 8192 contract limit (the worst such case, a single-strip RGBA TIFF,
// is estimated at ~1.17 GiB) while bounding the aggregate decode memory of the
// shared API process. Only 16-bit interlaced PNG / 16-bit TIFF and progressive
// full-chroma or CMYK JPEG above roughly 40-60 MP (depending on layout) exceed it.
const DefaultDecodeMemoryBudgetBytes int64 = 1280 << 20

// ErrDecodeBusy indicates Normalize could not be admitted to the decode memory
// budget before its context ended because other images were being decoded.
var ErrDecodeBusy = errors.New("image decoder is busy")

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

// DecodeBudget bounds the summed estimated working set of in-flight Normalize
// calls. Decoding is admitted only once the estimate fits in the remaining
// budget; an image whose estimate alone exceeds the whole budget is rejected
// with ErrDimensionsTooLarge. Waiting honors the caller's context.
//
// A DecodeBudget is safe for concurrent use. One budget should be shared by
// every Normalize caller in a process so the bound is process-wide.
type DecodeBudget struct {
	capacity int64
	sem      *semaphore.Weighted
}

// defaultDecodeBudget backs the package-level Normalize helper.
var defaultDecodeBudget = NewDecodeBudget(DefaultDecodeMemoryBudgetBytes)

// NewDecodeBudget returns a DecodeBudget of capacityBytes. Non-positive values
// fall back to DefaultDecodeMemoryBudgetBytes.
func NewDecodeBudget(capacityBytes int64) *DecodeBudget {
	if capacityBytes <= 0 {
		capacityBytes = DefaultDecodeMemoryBudgetBytes
	}
	return &DecodeBudget{capacity: capacityBytes, sem: semaphore.NewWeighted(capacityBytes)}
}

// Capacity reports the budget size in bytes.
func (b *DecodeBudget) Capacity() int64 {
	if b == nil {
		return DefaultDecodeMemoryBudgetBytes
	}
	return b.capacity
}

// acquire reserves cost bytes, blocking until they are available or ctx ends.
// The returned release function must be called exactly once.
func (b *DecodeBudget) acquire(ctx context.Context, cost int64) (func(), error) {
	if cost > b.capacity {
		return nil, errors.Wrapf(ErrDimensionsTooLarge,
			"estimated decode working set %d bytes exceeds the %d byte decode memory budget", cost, b.capacity)
	}
	if cost < 1 {
		cost = 1
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(ErrDecodeBusy, err.Error())
	}
	if err := b.sem.Acquire(ctx, cost); err != nil {
		return nil, errors.Wrap(ErrDecodeBusy, err.Error())
	}
	return func() { b.sem.Release(cost) }, nil
}

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
	case mimeImagePng:
		if err := checkPNGPixelData(raw, cfg); err != nil {
			return 0, errors.WithStack(err)
		}
		decoded = pixels * bytesPerPixel(cfg.ColorModel)
		if pngInterlaced(raw) {
			// Each Adam7 pass is decoded into its own image before being merged.
			decoded *= 2
		}
	case mimeImageGif:
		// Only the first frame is decoded and it cannot exceed the logical screen.
		decoded = pixels
	case mimeImageBmp:
		if err := checkBMPPixelData(raw, cfg); err != nil {
			return 0, errors.WithStack(err)
		}
		decoded = pixels * bytesPerPixel(cfg.ColorModel)
	case mimeImageTiff:
		// The image plus one decompressed strip or tile, which may span the
		// image and is read into a growing buffer (up to 2x its final size).
		decoded = 3 * pixels * bytesPerPixel(cfg.ColorModel)
	case mimeImageWebp:
		decoded = estimateWebPDecodeBytes(cfg)
	default:
		decoded = pixels * worstCaseBytesPerPixel
	}
	return decoded + resizeAndEncodeBytes(cfg.Width, cfg.Height) + fixedDecodeOverheadBytes, nil
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
	var resize int64
	if outW != w || outH != h {
		resize = int64(outW)*int64(h)*resizeSampleBytes + int64(outW)*int64(outH)*4
	}
	// The encoder's growing output buffer can hold up to ~2x the encoded size,
	// which itself is bounded by the raw size of the widest pixel format.
	encode := 2 * int64(outW) * int64(outH) * worstCaseBytesPerPixel
	return resize + encode
}

// resizedDimensions returns the dimensions resizeIfNeeded produces.
func resizedDimensions(w, h, maxEdge int) (int, int) {
	longest := max(w, h)
	if longest <= maxEdge {
		return w, h
	}
	return max(w*maxEdge/longest, 1), max(h*maxEdge/longest, 1)
}

// pngInterlaced reports whether the IHDR chunk declares Adam7 interlacing.
// The interlace method is the last byte of the 13-byte IHDR payload, which
// starts after the 8-byte signature and the 8-byte chunk length/type.
func pngInterlaced(raw []byte) bool {
	const interlaceOffset = 8 + 8 + 12
	return len(raw) > interlaceOffset && raw[interlaceOffset] != 0
}

// deflateMaxRatio is the theoretical DEFLATE/zlib expansion limit: one
// length/distance pair needs at least two bits and yields at most 258 bytes.
// See docs/ref/image_decode_limits.md.
const deflateMaxRatio = 1032

// checkPNGPixelData rejects PNGs too short to inflate to the pixel rows their
// IHDR declares. image/png allocates the whole image before inflating, so a
// tiny truncated file claiming 8192 x 8192 x 64 bit would otherwise allocate
// 512 MiB and then fail. Every valid PNG satisfies this bound.
func checkPNGPixelData(raw []byte, cfg image.Config) error {
	const bitDepthOffset, colorTypeOffset = 8 + 8 + 8, 8 + 8 + 9
	if len(raw) <= colorTypeOffset {
		return errors.Wrap(ErrDecodeFailed, "png header truncated")
	}
	var channels int64
	switch raw[colorTypeOffset] {
	case 0, 3: // grayscale, palette
		channels = 1
	case 4: // grayscale + alpha
		channels = 2
	case 2: // truecolor
		channels = 3
	case 6: // truecolor + alpha
		channels = 4
	default:
		return errors.Wrap(ErrDecodeFailed, "png color type invalid")
	}
	bitsPerPixel := channels * int64(raw[bitDepthOffset])
	rowBytes := (int64(cfg.Width)*bitsPerPixel + 7) / 8
	if need := rowBytes * int64(cfg.Height); int64(len(raw))*deflateMaxRatio < need {
		return errors.Wrapf(ErrDecodeFailed, "png pixel data truncated: %d bytes cannot inflate to %d", len(raw), need)
	}
	return nil
}

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

// estimateWebPDecodeBytes bounds golang.org/x/image/webp allocations: lossy
// frames are macroblock-padded 4:2:0 YCbCr; an alpha plane may itself be a
// VP8L image decoded to NRGBA; lossless images are NRGBA plus one inverse
// transform buffer of the same size.
func estimateWebPDecodeBytes(cfg image.Config) int64 {
	pixels := int64(cfg.Width) * int64(cfg.Height)
	switch cfg.ColorModel {
	case color.YCbCrModel:
		return 2 * pixels
	case color.NYCbCrAModel:
		return 2*pixels + pixels + 8*pixels
	default:
		return 8 * pixels
	}
}

// jpegComponent is the sampling of one JPEG frame component.
type jpegComponent struct {
	id   byte
	h, v int64
}

// jpegFrame is the subset of a JPEG frame header the estimator needs.
type jpegFrame struct {
	progressive bool
	adobe       bool
	components  []jpegComponent
}

// estimateJPEGDecodeBytes mirrors image/jpeg allocations: MCU-padded component
// planes (full-resolution chroma for non-standard sampling), a black plane for
// four-component images, a final CMYK/RGBA conversion where applicable, and
// for progressive images one 256-byte coefficient block per 8x8 block.
func estimateJPEGDecodeBytes(raw []byte, cfg image.Config) int64 {
	w, h := int64(cfg.Width), int64(cfg.Height)
	frame, ok := scanJPEGFrame(raw)
	if !ok {
		// Unknown layout: assume full-resolution progressive components plus conversion.
		n := int64(3)
		switch cfg.ColorModel {
		case color.GrayModel:
			n = 1
		case color.CMYKModel:
			n = 4
		}
		padded := (w + 15) / 16 * 16 * ((h + 15) / 16 * 16)
		return n*padded + n*padded*4 + 4*w*h
	}

	comps := frame.components
	if len(comps) == 1 {
		// image/jpeg ignores the sampling factors of single-component images.
		comps = []jpegComponent{{id: comps[0].id, h: 1, v: 1}}
	}
	var maxH, maxV int64 = 1, 1
	for _, c := range comps {
		maxH, maxV = max(maxH, c.h), max(maxV, c.v)
	}
	mxx := (w + 8*maxH - 1) / (8 * maxH)
	myy := (h + 8*maxV - 1) / (8 * maxV)
	lumaPadded := 8 * maxH * mxx * 8 * maxV * myy

	var planes, coefficients int64
	for _, c := range comps {
		coefficients += mxx * myy * c.h * c.v * jpegBlockBytes
	}
	switch len(comps) {
	case 1:
		planes = lumaPadded
	default:
		planes = lumaPadded + 2*jpegChromaPlaneBytes(comps, lumaPadded, maxH, maxV)
		if len(comps) == 4 {
			planes += 64 * comps[3].h * comps[3].v * mxx * myy
		}
	}

	var conversion int64
	if len(comps) == 4 || (len(comps) == 3 && (frame.adobe || jpegLooksRGB(comps))) {
		conversion = 4 * w * h
	}
	if !frame.progressive {
		coefficients = 0
	}
	return planes + conversion + coefficients
}

// jpegChromaPlaneBytes returns the size of one chroma plane. image/jpeg only
// subsamples chroma for the standard ratios where Y has the maximum factors and
// Cb/Cr share theirs; every other layout ("flex" mode) keeps full resolution.
func jpegChromaPlaneBytes(comps []jpegComponent, lumaPadded, maxH, maxV int64) int64 {
	if len(comps) < 3 || comps[0].h != maxH || comps[0].v != maxV || comps[1].h != comps[2].h || comps[1].v != comps[2].v {
		return lumaPadded
	}
	if maxH%comps[1].h != 0 || maxV%comps[1].v != 0 {
		return lumaPadded
	}
	return lumaPadded / ((maxH / comps[1].h) * (maxV / comps[1].v))
}

// jpegLooksRGB reports whether the component IDs spell R, G, B, in which case
// image/jpeg converts the planes to an RGBA image.
func jpegLooksRGB(comps []jpegComponent) bool {
	return len(comps) == 3 && comps[0].id == 'R' && comps[1].id == 'G' && comps[2].id == 'B'
}

// scanJPEGFrame walks the JPEG marker segments up to the first scan and
// returns the frame header plus whether an Adobe APP14 segment is present.
// It reports false when no frame header precedes the first scan.
func scanJPEGFrame(raw []byte) (jpegFrame, bool) {
	var (
		frame jpegFrame
		found bool
	)
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return frame, false
	}
	for i := 2; i+4 <= len(raw); {
		if raw[i] != 0xFF {
			return frame, found
		}
		marker := raw[i+1]
		if marker == 0xFF {
			i++ // fill byte
			continue
		}
		i += 2
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8) {
			continue // standalone markers carry no length
		}
		if marker == 0xDA || marker == 0xD9 {
			return frame, found // start of scan or end of image
		}
		if i+2 > len(raw) {
			return frame, found
		}
		segLen := int(raw[i])<<8 | int(raw[i+1])
		if segLen < 2 || i+segLen > len(raw) {
			return frame, found
		}
		seg := raw[i+2 : i+segLen]
		switch {
		case marker == 0xEE:
			if len(seg) >= 5 && string(seg[:5]) == "Adobe" {
				frame.adobe = true
			}
		case isJPEGFrameMarker(marker) && !found:
			parsed, ok := parseJPEGFrameHeader(marker, seg)
			if !ok {
				return frame, false
			}
			parsed.adobe = frame.adobe
			frame, found = parsed, true
		}
		i += segLen
	}
	return frame, found
}

// isJPEGFrameMarker reports whether marker is a start-of-frame marker.
func isJPEGFrameMarker(marker byte) bool {
	return marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
}

// parseJPEGFrameHeader decodes a start-of-frame segment payload.
func parseJPEGFrameHeader(marker byte, seg []byte) (jpegFrame, bool) {
	if len(seg) < 6 {
		return jpegFrame{}, false
	}
	n := int(seg[5])
	if n < 1 || n > 4 || len(seg) < 6+3*n {
		return jpegFrame{}, false
	}
	frame := jpegFrame{
		// SOF2, SOF6, SOF10 and SOF14 are the progressive frame types.
		progressive: marker == 0xC2 || marker == 0xC6 || marker == 0xCA || marker == 0xCE,
		components:  make([]jpegComponent, 0, n),
	}
	for c := range n {
		sampling := seg[7+3*c]
		hf, vf := int64(sampling>>4), int64(sampling&0x0F)
		if hf < 1 || hf > 4 || vf < 1 || vf > 4 {
			return jpegFrame{}, false
		}
		frame.components = append(frame.components, jpegComponent{id: seg[6+3*c], h: hf, v: vf})
	}
	return frame, true
}
