package imageproc

import (
	"encoding/binary"
	"image"

	errors "github.com/Laisky/errors/v2"
)

// pngBytesPerPixel returns the size of one pixel of the image image/png
// returns. DecodeConfig reports a gray model for grayscale PNGs, but with a
// tRNS chunk the decoder returns NRGBA (8-bit) or NRGBA64 (16-bit) instead.
// When the chunk walk cannot tell (it ran off the data before the first
// IDAT), the transparent layout is charged: an unknown is never taken as
// proof that there is no tRNS.
func pngBytesPerPixel(raw []byte, cfg image.Config) int64 {
	const bitDepthOffset, colorTypeOffset = 8 + 8 + 8, 8 + 8 + 9
	bpp := bytesPerPixel(cfg.ColorModel)
	if len(raw) > colorTypeOffset && raw[colorTypeOffset] == 0 && pngGrayTransparency(raw) != pngOpaque {
		if raw[bitDepthOffset] == 16 {
			return 8
		}
		return 4
	}
	return bpp
}

// pngTransparency is the outcome of the tRNS chunk walk.
type pngTransparency int

const (
	// pngOpaque means the walk reached the first IDAT (or IEND) without a tRNS.
	pngOpaque pngTransparency = iota
	// pngTransparent means a tRNS chunk precedes the first IDAT.
	pngTransparent
	// pngTransparencyUnknown means the walk ran off the data first.
	pngTransparencyUnknown
)

// pngGrayTransparency walks the chunk headers that precede the first IDAT,
// skipping their data by length without allocating, and reports whether a
// tRNS chunk is among them. image/png accepts any number of ancillary chunks
// there, so the walk is bounded only by the input (at least 12 bytes per
// chunk), never by a chunk count.
func pngGrayTransparency(raw []byte) pngTransparency {
	pos := int64(8)
	for pos+8 <= int64(len(raw)) {
		length := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		switch string(raw[pos+4 : pos+8]) {
		case "tRNS":
			return pngTransparent
		case "IDAT", "IEND":
			return pngOpaque
		}
		pos += 12 + length
	}
	return pngTransparencyUnknown
}

// pngInterlaced reports whether the IHDR chunk declares Adam7 interlacing.
// The interlace method is the last byte of the 13-byte IHDR payload, which
// starts after the 8-byte signature and the 8-byte chunk length/type.
func pngInterlaced(raw []byte) bool {
	const interlaceOffset = 8 + 8 + 12
	return len(raw) > interlaceOffset && raw[interlaceOffset] != 0
}

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
