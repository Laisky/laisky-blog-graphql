package imageproc

import (
	"encoding/binary"
	"image"

	errors "github.com/Laisky/errors/v2"
)

// WebP allocation model of golang.org/x/image/webp (v0.46). The decoder
// follows the image chunk, not the color model DecodeConfig reports: an
// extended (VP8X) file without the alpha flag reports YCbCr even when its
// image chunk is lossless VP8L, so the estimate walks the RIFF chunks.
const (
	// webpLossyBytesPerPixel covers the macroblock-padded 4:2:0 YCbCr frame
	// (1.5 bytes per padded pixel) and the per-macroblock filter state.
	webpLossyBytesPerPixel = 2
	// webpLosslessQuarterBytesPerPixel is 4x the bytes per pixel of a VP8L
	// decode: the transformed pixels (4 B/px, or 2 B/px when the
	// color-indexing transform bundles two pixels per byte) plus the full
	// 4 B/px copy that inverse color indexing allocates, plus predictor,
	// cross-color and entropy sub-images of at most 1/4 B/px each:
	// (2 + 4 + 3/4) B/px = 27/4.
	webpLosslessQuarterBytesPerPixel = 27
	// webpAlphaExtractBytesPerPixel is the alpha plane extracted from a
	// VP8L-compressed ALPH chunk (its NRGBA image is decoded first).
	webpAlphaExtractBytesPerPixel = 1
	// vp8lMaxHuffmanGroups is the decoder's limit on Huffman groups
	// (maxHuffImageSize, as in libwebp).
	vp8lMaxHuffmanGroups = 2600
	// vp8lMaxGroupSymbols is the alphabet of one Huffman group with the
	// largest color cache: green and length codes (256 + 24 + 2^11), red,
	// blue and alpha (256 each) and distance codes (40).
	vp8lMaxGroupSymbols = 256 + 24 + 1<<11 + 3*256 + 40
	// vp8lMaxGroupBytes bounds one group: tree nodes (2n-1 of 8 bytes per
	// tree), the code lengths and codes decoded for it (4 bytes each per
	// symbol), and its five trees' look-up tables (536 bytes each).
	vp8lMaxGroupBytes = 2*vp8lMaxGroupSymbols*8 + 2*vp8lMaxGroupSymbols*4 + 5*536
	// vp8lUnusedGroupBytes is what a decoded but unreferenced group costs
	// (its code lengths only).
	vp8lUnusedGroupBytes = vp8lMaxGroupSymbols * 4
)

// webpAlpha is how the alpha plane of a lossy WebP is stored.
type webpAlpha int

const (
	// webpAlphaNone means no ALPH chunk precedes the image chunk.
	webpAlphaNone webpAlpha = iota
	// webpAlphaRaw means an uncompressed ALPH chunk (1 byte per pixel).
	webpAlphaRaw
	// webpAlphaLossless means a VP8L-compressed ALPH chunk, or one whose
	// compression the walk could not read.
	webpAlphaLossless
)

// webpLayout is what the RIFF chunk walk learned about a WebP file.
type webpLayout struct {
	// known reports whether the walk reached the image chunk.
	known bool
	// lossless reports whether the image chunk is VP8L.
	lossless bool
	// vp8Bytes is the declared length of a lossy VP8 chunk; the decoder
	// allocates all of it (up to 8 partitions of 16 MiB) before reading.
	vp8Bytes int64
	// alpha is how a lossy image's alpha plane is stored.
	alpha webpAlpha
}

// scanWebP walks the RIFF chunks of raw up to the first image chunk (VP8 or
// VP8L), reading chunk headers only. A VP8 chunk that declares more data
// than the file holds can never decode, so it is rejected as ErrDecodeFailed
// before the decoder allocates the declared size. A walk that runs off the
// data returns an unknown layout, which the estimate charges conservatively.
func scanWebP(raw []byte) (webpLayout, error) {
	var layout webpLayout
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		return layout, nil
	}
	size := int64(len(raw))
	for pos := int64(12); pos+8 <= size; {
		id := string(raw[pos : pos+4])
		length := int64(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		data := pos + 8
		switch id {
		case "VP8 ":
			if data+length > size {
				return layout, errors.Wrapf(ErrDecodeFailed, "webp VP8 chunk truncated: declares %d bytes, %d present", length, size-data)
			}
			layout.known, layout.vp8Bytes = true, length
			return layout, nil
		case "VP8L":
			layout.known, layout.lossless = true, true
			return layout, nil
		case "ALPH":
			layout.alpha = webpAlphaLossless
			if length >= 1 && data < size && raw[data]&0x03 == 0 {
				layout.alpha = webpAlphaRaw
			}
		}
		pos = data + length + length&1
	}
	return layout, nil
}

// vp8lHuffmanBytes bounds the Huffman tables of one top-level VP8L decode of
// a w x h image. The entropy image has one entry per 4 x 4 tile at most, so
// at most min(tiles, 2600) groups are referenced; up to 2600 more may be
// decoded unreferenced.
func vp8lHuffmanBytes(w, h int64) int64 {
	tiles := ((w + 3) / 4) * ((h + 3) / 4)
	used := min(tiles, vp8lMaxHuffmanGroups)
	return min(int64(vp8lMaxHuffmanGroups)*vp8lMaxGroupBytes, used*vp8lMaxGroupBytes+vp8lMaxHuffmanGroups*vp8lUnusedGroupBytes)
}

// estimateWebPDecodeBytes bounds golang.org/x/image/webp allocations for
// layout: lossy frames are padded 4:2:0 YCbCr plus the whole declared VP8
// chunk; an alpha plane is either raw (1 B/px) or a VP8L image decoded to
// NRGBA and extracted; lossless images are a VP8L decode. Every VP8L decode
// also builds up to vp8lHuffmanBytes of Huffman tables. The alpha plane is
// charged whenever an ALPH chunk precedes the image chunk, because the
// decoder decodes it before it looks at the image chunk.
func estimateWebPDecodeBytes(layout webpLayout, cfg image.Config) int64 {
	w, h := int64(cfg.Width), int64(cfg.Height)
	pixels := w * h
	lossless := webpLosslessQuarterBytesPerPixel*pixels/4 + vp8lHuffmanBytes(w, h)
	losslessAlpha := lossless + webpAlphaExtractBytesPerPixel*pixels
	var alpha int64
	switch layout.alpha {
	case webpAlphaNone:
	case webpAlphaRaw:
		alpha = pixels
	case webpAlphaLossless:
		alpha = losslessAlpha
	}
	switch {
	case !layout.known:
		// Without an image chunk the decoder fails after at most an alpha
		// plane; charge the costliest layout anyway.
		return webpLossyBytesPerPixel*pixels + losslessAlpha
	case layout.lossless:
		return lossless + alpha
	default:
		return webpLossyBytesPerPixel*pixels + layout.vp8Bytes + alpha
	}
}
