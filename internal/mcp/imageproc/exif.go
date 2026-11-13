package imageproc

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
)

// readJPEGOrientation extracts the EXIF Orientation tag (0x0112) from a JPEG
// payload. It returns 1 (no rotation) when no EXIF data is present or the tag
// is absent. The parser is intentionally minimal — it only understands the
// subset of EXIF required to orient photos taken on mobile devices.
func readJPEGOrientation(data []byte) int {
	// JPEG files start with FF D8 and are a sequence of markers.
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 < len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		// SOS (FF DA) marks the start of image data — EXIF precedes it.
		if marker == 0xDA {
			return 1
		}
		segmentLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segmentLen < 2 {
			return 1
		}
		segStart := i + 4
		segEnd := i + 2 + segmentLen
		if segEnd > len(data) {
			return 1
		}
		// APP1 marker (FF E1) holds EXIF: look for "Exif\x00\x00" prefix.
		if marker == 0xE1 {
			if orient := parseAPP1(data[segStart:segEnd]); orient > 0 {
				return orient
			}
		}
		i = segEnd
	}
	return 1
}

// parseAPP1 parses an APP1 EXIF segment body and returns the Orientation value,
// or 0 if not found.
func parseAPP1(body []byte) int {
	if len(body) < 6 {
		return 0
	}
	if !bytes.Equal(body[:6], []byte("Exif\x00\x00")) {
		return 0
	}
	tiff := body[6:]
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch {
	case bytes.Equal(tiff[:2], []byte("II")):
		bo = binary.LittleEndian
	case bytes.Equal(tiff[:2], []byte("MM")):
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 0x002A {
		return 0
	}
	ifdOffset := int(bo.Uint32(tiff[4:8]))
	if ifdOffset+2 > len(tiff) {
		return 0
	}
	entryCount := int(bo.Uint16(tiff[ifdOffset : ifdOffset+2]))
	entries := tiff[ifdOffset+2:]
	// The count is untrusted: never index past the entries actually present.
	entryCount = min(entryCount, len(entries)/12)
	for idx := 0; idx < entryCount; idx++ {
		entry := entries[idx*12 : idx*12+12]
		tag := bo.Uint16(entry[0:2])
		if tag != 0x0112 {
			continue
		}
		typ := bo.Uint16(entry[2:4])
		// Orientation is a SHORT (3) — value is stored in the low 16 bits of the Value field.
		if typ != 3 {
			return 0
		}
		return int(bo.Uint16(entry[8:10]))
	}
	return 0
}

// applyOrientation rotates/flips src to compensate for the EXIF Orientation tag
// value. Values 1 (no-op) and anything outside 1..8 return src unchanged.
//
// It allocates exactly one RGBA image the size of src and converts each pixel
// once, without boxing colors, so its working set is predictable (see
// estimateDecodeBytes). The output is byte-identical to the previous
// implementation, which composed up to three full-size copies.
func applyOrientation(src image.Image, orientation int) image.Image {
	switch orientation {
	case 3:
		// The previous code converted with draw.Draw and then flipped the copy
		// twice; reversing the converted pixels in place is the same rotation.
		b := src.Bounds()
		dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		reversePixels(dst)
		return dst
	case 2, 4, 5, 6, 7, 8:
		return orientInto(src, orientation)
	default:
		return src
	}
}

// orientInto writes the oriented src into one new RGBA image. Pixel (x, y)
// of src, relative to its bounds, moves to the position the orientation
// defines; transposing orientations (5-8) swap the dimensions.
func orientInto(src image.Image, orientation int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dstW, dstH := w, h
	if orientation >= 5 {
		dstW, dstH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	at := rgbaPixelReader(src)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			default: // 8: rotate 270 clockwise
				dx, dy = y, w-1-x
			}
			c := at(b.Min.X+x, b.Min.Y+y)
			i := dst.PixOffset(dx, dy)
			dst.Pix[i+0], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c.R, c.G, c.B, c.A
		}
	}
	return dst
}

// rgbaPixelReader returns a function yielding src pixels converted exactly as
// color.RGBAModel converts them (16-bit RGBA shifted to 8 bits). The decoder
// output types get typed accessors, which avoid one heap-boxed color per pixel.
func rgbaPixelReader(src image.Image) func(x, y int) color.RGBA {
	switch s := src.(type) {
	case *image.RGBA:
		return s.RGBAAt
	case *image.YCbCr:
		return func(x, y int) color.RGBA { return toRGBA8(s.YCbCrAt(x, y)) }
	case *image.Gray:
		return func(x, y int) color.RGBA { return toRGBA8(s.GrayAt(x, y)) }
	case *image.CMYK:
		return func(x, y int) color.RGBA { return toRGBA8(s.CMYKAt(x, y)) }
	case *image.NRGBA:
		return func(x, y int) color.RGBA { return toRGBA8(s.NRGBAAt(x, y)) }
	default:
		return func(x, y int) color.RGBA { return toRGBA8(src.At(x, y)) }
	}
}

// toRGBA8 mirrors color.RGBAModel.Convert for a concrete color value.
func toRGBA8[C interface{ RGBA() (r, g, b, a uint32) }](c C) color.RGBA {
	r, g, b, a := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)} //nolint:gosec // RGBA() values are at most 0xffff
}

// reversePixels reverses the pixel order of img, which rotates it by 180
// degrees because its rows are contiguous (it starts at the origin).
func reversePixels(img *image.RGBA) {
	pix := img.Pix
	for i, j := 0, len(pix)-4; i < j; i, j = i+4, j-4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3], pix[j], pix[j+1], pix[j+2], pix[j+3] =
			pix[j], pix[j+1], pix[j+2], pix[j+3], pix[i], pix[i+1], pix[i+2], pix[i+3]
	}
}
