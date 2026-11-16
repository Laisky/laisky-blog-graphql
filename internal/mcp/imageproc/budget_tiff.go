package imageproc

import (
	"encoding/binary"
	"image"
)

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
//
// Tile edges and byte counts are untrusted 32-bit values, so every product
// saturates (satMul) instead of wrapping: a geometry the decoder cannot
// represent (it refuses tiles whose byte size overflows) is charged more than
// any budget rather than a wrapped, possibly tiny, cost.
func tiffBlockBufferBytes(layout tiffLayout, rawLen, pixels int64) int64 {
	if !layout.ok {
		return satMul(growingBufferFactor, satMul(pixels, tiffDecoderBytesPerPixel))
	}
	blockMax := satMul(layout.blockPixels, tiffDecoderBytesPerPixel)
	switch layout.compression {
	case 0, 1: // none
		return satAdd(satMul(2, rawLen), tiffSafeReadChunk)
	case 8, 32946: // deflate; one buffer is reused across blocks
		return satMul(growingBufferFactor, min(blockMax, satAdd(satMul(deflateMaxRatio, layout.maxBlockBytes), 64<<10)))
	case 32773: // PackBits; each block gets a fresh buffer
		return satMul(growingBufferFactor, min(satMul(layout.blocks, blockMax),
			satAdd(satMul(packBitsMaxRatio, rawLen), satMul(layout.blocks, 128))))
	default: // LZW, CCITT; one buffer is reused across blocks
		return satMul(growingBufferFactor, blockMax)
	}
}

// parseTIFFLayout reads compression, strip/tile geometry and block byte
// counts from the first IFD of a classic TIFF, mirroring how
// golang.org/x/image/tiff derives its block size. Every read is bounds
// checked and every product of header values saturates; ok is false when the
// IFD cannot be parsed.
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
		layout.blockPixels = satMul(tileW, tileH)
		layout.blocks = satMul((w+tileW-1)/tileW, (h+tileH-1)/tileH)
		countsEntry = tileCounts
	} else {
		blockH := h
		if rowsPerStrip > 0 && rowsPerStrip < h {
			blockH = rowsPerStrip
		}
		layout.blockPixels = satMul(w, blockH)
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
