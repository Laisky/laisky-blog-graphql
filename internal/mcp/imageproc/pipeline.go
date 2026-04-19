package imageproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

const (
	// MaxDecodeDimension is the largest pixel edge accepted at decode time.
	// Bytes exceeding this on either axis are rejected as decode bombs.
	MaxDecodeDimension = 8192
	// NormalizedMaxEdge is the canonical longest-edge cap after resizing.
	NormalizedMaxEdge = 1536
)

// PipelineResult is the output of Normalize: PNG bytes and companion metadata.
type PipelineResult struct {
	PNG          []byte
	Width        int
	Height       int
	SHA256       string
	OriginalMIME string
}

// AllowedInputMIMETypes is the closed set of MIME types Normalize will accept.
var AllowedInputMIMETypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
	"image/bmp":  {},
	"image/tiff": {},
	"image/gif":  {},
}

// Normalize decodes raw image bytes, fixes orientation, resizes to
// NormalizedMaxEdge, and re-encodes as PNG. originalFilename is only used to
// enrich the MIME detection heuristic; it may be empty.
func Normalize(raw []byte, originalFilename string) (PipelineResult, error) {
	if len(raw) == 0 {
		return PipelineResult{}, errors.Wrap(ErrDecodeFailed, "imageproc normalize: empty body")
	}

	mime, err := detectMIME(raw, originalFilename)
	if err != nil {
		return PipelineResult{}, errors.WithStack(err)
	}

	if _, ok := AllowedInputMIMETypes[mime]; !ok {
		return PipelineResult{}, errors.Wrapf(ErrUnsupportedMIME, "imageproc normalize: %s", mime)
	}

	// Decode config early so we can reject pathological dimensions before
	// allocating a full pixel buffer.
	cfg, cfgFormat, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return PipelineResult{}, errors.Wrap(ErrDecodeFailed, "imageproc normalize: decode config")
	}
	if cfg.Width > MaxDecodeDimension || cfg.Height > MaxDecodeDimension {
		return PipelineResult{}, errors.Wrapf(ErrDimensionsTooLarge, "imageproc normalize: %dx%d", cfg.Width, cfg.Height)
	}
	_ = cfgFormat

	img, err := decodeImage(raw, mime)
	if err != nil {
		return PipelineResult{}, errors.Wrap(ErrDecodeFailed, "imageproc normalize: decode")
	}

	if mime == "image/jpeg" {
		orientation := readJPEGOrientation(raw)
		img = applyOrientation(img, orientation)
	}

	resized := resizeIfNeeded(img, NormalizedMaxEdge)

	var buf bytes.Buffer
	encoder := &png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&buf, resized); err != nil {
		return PipelineResult{}, errors.Wrap(err, "imageproc normalize: encode png")
	}

	pngBytes := buf.Bytes()
	sum := sha256.Sum256(pngBytes)

	b := resized.Bounds()
	return PipelineResult{
		PNG:          pngBytes,
		Width:        b.Dx(),
		Height:       b.Dy(),
		SHA256:       hex.EncodeToString(sum[:]),
		OriginalMIME: mime,
	}, nil
}

// detectMIME uses magic bytes (http.DetectContentType) to identify the image
// type. The filename is only consulted for formats (WebP, BMP, TIFF) that
// http.DetectContentType cannot disambiguate. A mismatch between the declared
// and detected type returns ErrUnsupportedMIME.
func detectMIME(raw []byte, originalFilename string) (string, error) {
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	detected := http.DetectContentType(head)
	// http.DetectContentType returns things like "image/jpeg; charset=utf-8" sometimes.
	if idx := strings.Index(detected, ";"); idx >= 0 {
		detected = detected[:idx]
	}
	detected = strings.TrimSpace(strings.ToLower(detected))

	if detected == "application/octet-stream" || detected == "" {
		// Fall back to filename-based sniffing for formats where magic bytes
		// are ambiguous (WebP vs GIF RIFF headers are rare but possible).
		switch strings.ToLower(filenameExt(originalFilename)) {
		case ".webp":
			if isWebP(raw) {
				return "image/webp", nil
			}
		case ".bmp":
			return "image/bmp", nil
		case ".tiff", ".tif":
			return "image/tiff", nil
		case ".png":
			return "image/png", nil
		case ".jpg", ".jpeg":
			return "image/jpeg", nil
		case ".gif":
			return "image/gif", nil
		}
		return "", errors.Wrap(ErrUnsupportedMIME, "imageproc: could not identify MIME")
	}

	// http.DetectContentType does not identify WebP — check manually.
	if detected == "image/webp" || isWebP(raw) {
		return "image/webp", nil
	}
	return detected, nil
}

// filenameExt returns the lowercase extension (including the dot) of name.
func filenameExt(name string) string {
	for i := len(name) - 1; i >= 0 && name[i] != '/' && name[i] != '\\'; i-- {
		if name[i] == '.' {
			return name[i:]
		}
	}
	return ""
}

// isWebP reports whether raw begins with the RIFF / WEBP magic.
func isWebP(raw []byte) bool {
	if len(raw) < 12 {
		return false
	}
	return bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP"))
}

// decodeImage decodes raw into an image.Image using the decoder matching mime.
// GIF inputs return only the first frame, consistent with the proposal.
func decodeImage(raw []byte, mime string) (image.Image, error) {
	r := bytes.NewReader(raw)
	switch mime {
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/png":
		return png.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	case "image/bmp":
		return bmp.Decode(r)
	case "image/tiff":
		return tiff.Decode(r)
	case "image/gif":
		g, err := gif.DecodeAll(r)
		if err != nil {
			return nil, err
		}
		if len(g.Image) == 0 {
			return nil, errors.New("gif has zero frames")
		}
		return g.Image[0], nil
	default:
		return nil, errors.Errorf("unsupported decoder MIME %q", mime)
	}
}

// resizeIfNeeded returns img untouched if its longest edge is already within
// maxEdge; otherwise it scales the image using Catmull-Rom resampling.
func resizeIfNeeded(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return img
	}
	newW := w * maxEdge / longest
	newH := h * maxEdge / longest
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// readAllCapped reads r into memory, returning ErrImageTooLarge as soon as
// the read would exceed cap.
func readAllCapped(r io.Reader, cap int64) ([]byte, error) {
	if cap <= 0 {
		return nil, errors.New("readAllCapped: cap must be positive")
	}
	limited := io.LimitReader(r, cap+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.Wrap(err, "read body")
	}
	if int64(len(buf)) > cap {
		return nil, errors.WithStack(ErrImageTooLarge)
	}
	return buf, nil
}
