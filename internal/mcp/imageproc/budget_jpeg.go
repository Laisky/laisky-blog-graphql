package imageproc

import (
	"bytes"
	"image"
	"image/color"
)

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

// scanJPEGFrame walks the JPEG marker segments up to the first scan the way
// image/jpeg's decode loop does, and returns the frame header plus whether
// an Adobe APP14 segment is present anywhere in the file. Like the decoder it
// skips bytes up to the next 0xFF and a stray "\xff\x00" pair without reading
// a length, so crafted padding cannot make the walk read a fake frame header
// that the decoder skips (review of PR #94). image/jpeg also processes APP14
// segments after the first scan, and one there still triggers the RGBA
// conversion, so wherever the walk stops the rest of the file is searched for
// such a segment; entropy-coded data never contains a bare 0xFF 0xEE, so a
// false hit is rare and only overcharges. It reports false when no frame
// header precedes the first scan, or when the decoder would fail before it.
func scanJPEGFrame(raw []byte) (jpegFrame, bool) {
	var (
		frame jpegFrame
		found bool
	)
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return frame, false
	}
	stop := func(at int) (jpegFrame, bool) {
		frame.adobe = frame.adobe || jpegHasAdobeSegment(raw[min(at, len(raw)):])
		return frame, found
	}
	for i := 2; ; {
		marker, next, ok := nextJPEGMarker(raw, i)
		i = next
		if !ok {
			return stop(i)
		}
		switch {
		case marker == 0xD9: // end of image
			return stop(i)
		case marker >= 0xD0 && marker <= 0xD7: // restart markers carry no length
			continue
		}
		if i+2 > len(raw) {
			return stop(i)
		}
		segLen := int(raw[i])<<8 | int(raw[i+1])
		if segLen < 2 || i+segLen > len(raw) {
			return stop(i)
		}
		seg := raw[i+2 : i+segLen]
		switch {
		case marker == 0xDA: // start of scan
			return stop(i)
		case marker == 0xEE:
			if len(seg) >= 12 && string(seg[:5]) == "Adobe" {
				frame.adobe = true
			}
		case isJPEGFrameMarker(marker):
			if found {
				return stop(i) // the decoder rejects a second frame header
			}
			parsed, ok := parseJPEGFrameHeader(marker, seg)
			if !ok {
				return frame, false
			}
			parsed.adobe = frame.adobe
			frame, found = parsed, true
		case marker < 0xC0 || (marker > 0xEF && marker != 0xFE) || marker == 0xC8:
			// The decoder fails on markers it does not know; report an
			// unknown layout, which the estimate charges conservatively.
			return frame, false
		}
		i += segLen
	}
}

// nextJPEGMarker finds the next marker at or after i as image/jpeg's decode
// loop does: bytes up to the next 0xFF and stray "\xff\x00" pairs are
// extraneous data, and 0xFF fill bytes may precede a marker. It returns the
// marker, the position after it, and false when the data ends first.
func nextJPEGMarker(raw []byte, i int) (byte, int, bool) {
	for i+2 <= len(raw) {
		switch {
		case raw[i] != 0xFF:
			i++
		case raw[i+1] == 0x00:
			i += 2
		default:
			i++
			for i < len(raw) && raw[i] == 0xFF {
				i++
			}
			if i >= len(raw) {
				return 0, i, false
			}
			return raw[i], i + 1, true
		}
	}
	return 0, len(raw), false
}

// jpegHasAdobeSegment reports whether data contains an APP14 segment that
// carries the Adobe signature image/jpeg looks for (a 12-byte payload
// starting with "Adobe").
func jpegHasAdobeSegment(data []byte) bool {
	marker := []byte{0xFF, 0xEE}
	for at := 0; ; {
		idx := bytes.Index(data[at:], marker)
		if idx < 0 {
			return false
		}
		seg := data[at+idx:]
		if len(seg) >= 4+12 && int(seg[2])<<8|int(seg[3]) >= 2+12 && string(seg[4:9]) == "Adobe" {
			return true
		}
		at += idx + 1
	}
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
