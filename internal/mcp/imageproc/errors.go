// Package imageproc implements the server-side image normalization pipeline
// and the SSRF-guarded URL fetcher that feeds it. The pipeline decodes many
// input formats, resizes them to a canonical max dimension, and re-encodes
// the result as PNG so MinIO objects and MCP responses only ever carry a
// single, universally-supported format.
package imageproc

import errors "github.com/Laisky/errors/v2"

// ErrUnsupportedMIME indicates the uploaded bytes are not a supported image type.
var ErrUnsupportedMIME = errors.New("unsupported image MIME type")

// ErrImageTooLarge indicates the raw body exceeded the configured per-image cap.
var ErrImageTooLarge = errors.New("image exceeds size limit")

// ErrDimensionsTooLarge indicates the image exceeded the decode-bomb dimension guard.
var ErrDimensionsTooLarge = errors.New("image dimensions exceed decode limit")

// ErrDecodeFailed indicates the bytes could not be decoded as the declared MIME type.
var ErrDecodeFailed = errors.New("image decode failed")

// ErrURLBlocked indicates the target URL resolves to a private / unsafe destination
// or uses a disabled scheme.
var ErrURLBlocked = errors.New("image URL is blocked")

// ErrURLFetchFailed indicates a transport-level failure while fetching an image URL.
var ErrURLFetchFailed = errors.New("image URL fetch failed")

// ErrURLTimeout indicates the fetch exceeded the configured deadline.
var ErrURLTimeout = errors.New("image URL fetch timed out")
