package imageproc

import (
	"encoding/binary"
	"image"
	"image/color"

	errors "github.com/Laisky/errors/v2"
)

// DefaultDecodeMemoryBudgetBytes is the default process-wide budget for the
// estimated transient heap of concurrent Normalize calls: 1.25 GiB. It admits
// every 8-bit-per-channel input of any supported format and layout up to the
// 8192 x 8192 contract limit (the worst such case, a single-strip RGBA TIFF,
// is estimated at ~1.17 GiB) while bounding the aggregate decode memory of the
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
		decoded = pixels*bytesPerPixel(cfg.ColorModel) + tiffBlockBufferBytes(parseTIFFLayout(raw, cfg), int64(len(raw)), pixels)
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

// pngBytesPerPixel returns the size of one pixel of the image image/png
// returns. DecodeConfig reports a gray model for grayscale PNGs, but with a
// tRNS chunk the decoder returns NRGBA (8-bit) or NRGBA64 (16-bit) instead.
func pngBytesPerPixel(raw []byte, cfg image.Config) int64 {
	const bitDepthOffset, colorTypeOffset = 8 + 8 + 8, 8 + 8 + 9
	bpp := bytesPerPixel(cfg.ColorModel)
	if len(raw) > colorTypeOffset && raw[colorTypeOffset] == 0 && pngHasTRNS(raw) {
		if raw[bitDepthOffset] == 16 {
			return 8
		}
		return 4
	}
	return bpp
}

// pngMaxScannedChunks bounds the chunk walk of pngHasTRNS.
const pngMaxScannedChunks = 1024

// pngHasTRNS reports whether a tRNS chunk precedes the first IDAT. It walks
// chunk headers only (skipping their data by length), at most
// pngMaxScannedChunks of them, without allocating.
func pngHasTRNS(raw []byte) bool {
	pos := int64(8)
	for range pngMaxScannedChunks {
		if pos+8 > int64(len(raw)) {
			return false
		}
		length := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		switch string(raw[pos+4 : pos+8]) {
		case "tRNS":
			return true
		case "IDAT", "IEND":
			return false
		}
		pos += 12 + length
	}
	return false
}

// tiffLayout is the part of the first TIFF IFD that sizes the decoder's
// per-strip (or per-tile) inflate buffer.
type tiffLayout struct {
	ok            bool
	compression   int64
	blocks        int64 // strips or tiles
	blockPixels   int64 // pixels per strip or tile, as the decoder pads them
	maxBlockBytes int64 // largest StripByteCounts / TileByteCounts value
}

const (
	// tiffDecoderBytesPerPixel is the per-pixel ceiling golang.org/x/image/tiff
	// lets one block inflate to, whatever the real bit depth (RGBA64).
	tiffDecoderBytesPerPixel = 8
	// growingBufferFactor bounds the cumulative allocation of a bytes.Buffer or
	// append-grown slice that ends at n bytes: doubling allocates about 2n in
	// total and the final capacity may overshoot up to 2n.
	growingBufferFactor = 4
	// packBitsMaxRatio is PackBits' maximum expansion (a 2-byte run emits 128).
	packBitsMaxRatio = 64
	// tiffMaxBlockEntries bounds how many byte-count entries are inspected.
	tiffMaxBlockEntries = 1 << 20
	// tiffSafeReadChunk is the chunk x/image/tiff reads large uncompressed blocks in.
	tiffSafeReadChunk = 10 << 20
)

// tiffBlockBufferBytes bounds what golang.org/x/image/tiff allocates to read
// strips or tiles on top of the image. Uncompressed blocks are read as is, so
// they are bounded by the file. Compressed blocks are inflated into a buffer
// limited to blockPixels x 8 bytes (for every bit depth); deflate data cannot
// expand by more than deflateMaxRatio, PackBits by packBitsMaxRatio. LZW and
// CCITT get the decoder's own limit. Without a parsable layout the whole
// image is assumed to be one compressed block.
func tiffBlockBufferBytes(layout tiffLayout, rawLen, pixels int64) int64 {
	if !layout.ok {
		return growingBufferFactor * pixels * tiffDecoderBytesPerPixel
	}
	blockMax := layout.blockPixels * tiffDecoderBytesPerPixel
	switch layout.compression {
	case 0, 1: // none
		return 2*rawLen + tiffSafeReadChunk
	case 8, 32946: // deflate; one buffer is reused across blocks
		return growingBufferFactor * min(blockMax, deflateMaxRatio*layout.maxBlockBytes+64<<10)
	case 32773: // PackBits; each block gets a fresh buffer
		return growingBufferFactor * min(layout.blocks*blockMax, packBitsMaxRatio*rawLen+layout.blocks*128)
	default: // LZW, CCITT; one buffer is reused across blocks
		return growingBufferFactor * blockMax
	}
}

// parseTIFFLayout reads compression, strip/tile geometry and block byte
// counts from the first IFD of a classic TIFF, mirroring how
// golang.org/x/image/tiff derives its block size. Every read is bounds
// checked; ok is false when the IFD cannot be parsed.
func parseTIFFLayout(raw []byte, cfg image.Config) tiffLayout {
	var bo binary.ByteOrder
	switch {
	case len(raw) < 8:
		return tiffLayout{}
	case raw[0] == 'I' && raw[1] == 'I':
		bo = binary.LittleEndian
	case raw[0] == 'M' && raw[1] == 'M':
		bo = binary.BigEndian
	default:
		return tiffLayout{}
	}
	ifd := int64(bo.Uint32(raw[4:8]))
	if ifd+2 > int64(len(raw)) {
		return tiffLayout{}
	}
	count := int64(bo.Uint16(raw[ifd : ifd+2]))
	entries := raw[ifd+2:]
	count = min(count, int64(len(entries))/12)

	values := func(entry []byte) (func(i int64) (int64, bool), int64) {
		typ, n := bo.Uint16(entry[2:4]), int64(bo.Uint32(entry[4:8]))
		size := int64(0)
		switch typ {
		case 3: // SHORT
			size = 2
		case 4: // LONG
			size = 4
		default:
			return nil, 0
		}
		n = min(n, tiffMaxBlockEntries)
		data := entry[8:12]
		if n*size > 4 {
			off := int64(bo.Uint32(entry[8:12]))
			if off < 0 || off+n*size > int64(len(raw)) {
				return nil, 0
			}
			data = raw[off : off+n*size]
		}
		return func(i int64) (int64, bool) {
			if i >= n {
				return 0, false
			}
			if size == 2 {
				return int64(bo.Uint16(data[i*2:])), true
			}
			return int64(bo.Uint32(data[i*4:])), true
		}, n
	}
	first := func(entry []byte) int64 {
		at, _ := values(entry)
		if at == nil {
			return 0
		}
		v, _ := at(0)
		return v
	}

	layout := tiffLayout{ok: true, compression: 1}
	var rowsPerStrip, tileW, tileH int64
	var stripCounts, tileCounts []byte
	for i := int64(0); i < count; i++ {
		entry := entries[i*12 : i*12+12]
		switch bo.Uint16(entry[0:2]) {
		case 259:
			layout.compression = first(entry)
		case 278:
			rowsPerStrip = first(entry)
		case 322:
			tileW = first(entry)
		case 323:
			tileH = first(entry)
		case 279:
			stripCounts = entry
		case 325:
			tileCounts = entry
		}
	}

	w, h := int64(cfg.Width), int64(cfg.Height)
	countsEntry := stripCounts
	if tileW > 0 && tileH > 0 {
		layout.blockPixels = tileW * tileH
		layout.blocks = ((w + tileW - 1) / tileW) * ((h + tileH - 1) / tileH)
		countsEntry = tileCounts
	} else {
		blockH := h
		if rowsPerStrip > 0 && rowsPerStrip < h {
			blockH = rowsPerStrip
		}
		layout.blockPixels = w * blockH
		layout.blocks = (h + blockH - 1) / max(blockH, 1)
	}
	if countsEntry == nil {
		return tiffLayout{}
	}
	at, n := values(countsEntry)
	if at == nil {
		return tiffLayout{}
	}
	for i := int64(0); i < n; i++ {
		v, _ := at(i)
		layout.maxBlockBytes = max(layout.maxBlockBytes, v)
	}
	return layout
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
