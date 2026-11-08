package imageproc

import (
	"bytes"
	"context"
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
	mimeImageJpeg: {},
	mimeImagePng:  {},
	mimeImageWebp: {},
	mimeImageBmp:  {},
	mimeImageTiff: {},
	mimeImageGif:  {},
}

// Normalize decodes raw image bytes, fixes orientation, resizes to
// NormalizedMaxEdge, and re-encodes as PNG. originalFilename is only used to
// enrich the MIME detection heuristic; it may be empty. Decoding is admitted
// through the package default DecodeBudget; long-lived callers should share
// one explicitly configured budget and use DecodeBudget.Normalize instead.
func Normalize(ctx context.Context, raw []byte, originalFilename string) (PipelineResult, error) {
	return defaultDecodeBudget.Normalize(ctx, raw, originalFilename)
}

// Normalize runs the normalization pipeline after reserving the input's
// estimated decode working set from b. Only headers are parsed before the
// reservation, so pixel buffers are never allocated for rejected inputs.
// It returns ErrDimensionsTooLarge when the dimensions or the estimate exceed
// the limits, and ErrDecodeBusy when ctx ends while waiting for the budget.
func (b *DecodeBudget) Normalize(ctx context.Context, raw []byte, originalFilename string) (PipelineResult, error) {
	if b == nil {
		b = defaultDecodeBudget
	}
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

	// Decode the header with the same decoder that will decode the pixels, so
	// the size checks below describe exactly what decodeImage will allocate.
	cfg, err := decodeConfig(raw, mime)
	if err != nil {
		return PipelineResult{}, errors.Wrap(ErrDecodeFailed, "imageproc normalize: decode config")
	}
	if cfg.Width > MaxDecodeDimension || cfg.Height > MaxDecodeDimension {
		return PipelineResult{}, errors.Wrapf(ErrDimensionsTooLarge, "imageproc normalize: %dx%d", cfg.Width, cfg.Height)
	}

	cost, err := estimateDecodeBytes(raw, mime, cfg)
	if err != nil {
		return PipelineResult{}, errors.Wrap(err, "imageproc normalize: estimate decode memory")
	}
	release, err := b.acquire(ctx, cost)
	if err != nil {
		return PipelineResult{}, errors.Wrap(err, "imageproc normalize: admit decode")
	}
	defer release()

	img, err := decodeImage(raw, mime)
	if err != nil {
		return PipelineResult{}, errors.Wrap(ErrDecodeFailed, "imageproc normalize: decode")
	}

	if mime == mimeImageJpeg {
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

	bounds := resized.Bounds()
	return PipelineResult{
		PNG:          pngBytes,
		Width:        bounds.Dx(),
		Height:       bounds.Dy(),
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
				return mimeImageWebp, nil
			}
		case ".bmp":
			return mimeImageBmp, nil
		case ".tiff", ".tif":
			return mimeImageTiff, nil
		case ".png":
			return mimeImagePng, nil
		case ".jpg", ".jpeg":
			return mimeImageJpeg, nil
		case ".gif":
			return mimeImageGif, nil
		}
		return "", errors.Wrap(ErrUnsupportedMIME, "imageproc: could not identify MIME")
	}

	// http.DetectContentType does not identify WebP — check manually.
	if detected == mimeImageWebp || isWebP(raw) {
		return mimeImageWebp, nil
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

// decodeConfig reads only the header of raw with the decoder matching mime.
// For GIF it reports the logical screen, which bounds every frame.
func decodeConfig(raw []byte, mime string) (image.Config, error) {
	r := bytes.NewReader(raw)
	switch mime {
	case mimeImageJpeg:
		return jpeg.DecodeConfig(r)
	case mimeImagePng:
		return png.DecodeConfig(r)
	case mimeImageWebp:
		return webp.DecodeConfig(r)
	case mimeImageBmp:
		return bmp.DecodeConfig(r)
	case mimeImageTiff:
		return tiff.DecodeConfig(r)
	case mimeImageGif:
		return gif.DecodeConfig(r)
	default:
		return image.Config{}, errors.Errorf("unsupported decoder MIME %q", mime)
	}
}

// decodeImage decodes raw into an image.Image using the decoder matching mime.
// GIF inputs decode only the first frame, consistent with the proposal: the
// decoder stops reading after it, so later frames are neither parsed nor
// allocated.
func decodeImage(raw []byte, mime string) (image.Image, error) {
	r := bytes.NewReader(raw)
	switch mime {
	case mimeImageJpeg:
		return jpeg.Decode(r)
	case mimeImagePng:
		return png.Decode(r)
	case mimeImageWebp:
		return webp.Decode(r)
	case mimeImageBmp:
		return bmp.Decode(r)
	case mimeImageTiff:
		return tiff.Decode(r)
	case mimeImageGif:
		return gif.Decode(r)
	default:
		return nil, errors.Errorf("unsupported decoder MIME %q", mime)
	}
}

// resizeIfNeeded returns img untouched if its longest edge is already within
// maxEdge; otherwise it scales the image using Catmull-Rom resampling.
func resizeIfNeeded(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	newW, newH := resizedDimensions(b.Dx(), b.Dy(), maxEdge)
	if newW == b.Dx() && newH == b.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// readAllCapped reads r into memory, returning ErrImageTooLarge as soon as
// the read would exceed cap.
func readAllCapped(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("readAllCapped: limit must be positive")
	}
	limited := io.LimitReader(r, limit+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.Wrap(err, "read body")
	}
	if int64(len(buf)) > limit {
		return nil, errors.WithStack(ErrImageTooLarge)
	}
	return buf, nil
}
