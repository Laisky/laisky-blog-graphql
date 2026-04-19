package imageproc

import (
	"bytes"
	"encoding/binary"
	"image"
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
	for idx := 0; idx < entryCount; idx++ {
		entry := entries[idx*12 : idx*12+12]
		if len(entry) < 12 {
			return 0
		}
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
func applyOrientation(src image.Image, orientation int) image.Image {
	switch orientation {
	case 0, 1:
		return src
	case 2:
		return flipHorizontal(src)
	case 3:
		return rotate180(src)
	case 4:
		return flipVertical(src)
	case 5:
		return flipHorizontal(rotate90(src))
	case 6:
		return rotate90(src)
	case 7:
		return flipHorizontal(rotate270(src))
	case 8:
		return rotate270(src)
	default:
		return src
	}
}

func flipHorizontal(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dx()-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func flipVertical(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, b.Dy()-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate90(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dy()-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate180(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return flipHorizontal(flipVertical(dst))
}

func rotate270(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(y, b.Dx()-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
