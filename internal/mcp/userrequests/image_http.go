package userrequests

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
)

// maxMultipartMemory caps the RAM used by multipart parsing before the remainder
// spills to disk. The overall body cap is enforced separately by MaxBytesReader.
const maxMultipartMemory = 8 << 20 // 8 MiB

// parseMultipart walks a multipart/form-data request and returns the caller's
// text content, task_id, and the ordered list of attachments (files then URLs).
// File attachments are not read into memory here: each is opened from the
// parsed form (memory up to maxMultipartMemory, temporary files beyond) when
// ImageManager.Prepare reaches it, so at most one raw attachment is held at a
// time. The caller must call cleanup, which removes the temporary files, once
// the attachments are no longer needed; cleanup is a no-op on error.
func (h *httpHandler) parseMultipart(r *http.Request) (content, taskID string, attachments []AttachmentInput, cleanup func(), err error) {
	noop := func() {}
	const overhead int64 = 2 << 20
	perImage, count := DefaultImagePerImageMaxBytes, DefaultImageMaxPerRequest
	if h.imageManager != nil {
		settings := h.imageManager.Settings()
		perImage, count = settings.PerImageMaxBytes, settings.MaxPerRequest
	}
	if perImage <= 0 || count <= 0 || int64(count) > (math.MaxInt64-overhead)/perImage {
		return "", "", nil, noop, errors.New("invalid image request size limits")
	}
	bodyCap := perImage*int64(count) + overhead

	if err := parseBoundedMultipart(r, bodyCap); err != nil {
		return "", "", nil, noop, err
	}
	form := r.MultipartForm
	if form == nil {
		return "", "", nil, noop, errors.New("multipart form missing")
	}
	cleanup = func() {
		if err := form.RemoveAll(); err != nil {
			h.logger.Warn("remove multipart temporary files", zap.Error(err))
		}
	}
	content = strings.TrimSpace(firstFormValue(form.Value, "content"))
	taskID = strings.TrimSpace(firstFormValue(form.Value, "task_id"))

	attachmentCount := len(form.File["images"])
	for _, raw := range form.Value["image_urls"] {
		if strings.TrimSpace(raw) != "" {
			attachmentCount++
		}
	}
	if attachmentCount > count {
		cleanup()
		return "", "", nil, noop, errors.WithStack(ErrTooManyImages)
	}

	for _, hdr := range form.File["images"] {
		if hdr == nil {
			continue
		}
		if hdr.Size > perImage {
			cleanup()
			return "", "", nil, noop, errors.WithStack(imageproc.ErrImageTooLarge)
		}
		attachments = append(attachments, AttachmentInput{
			Filename: hdr.Filename,
			Open: func() (io.ReadCloser, error) {
				file, err := hdr.Open()
				if err != nil {
					return nil, errors.Wrap(err, "open multipart part")
				}
				return file, nil
			},
		})
	}
	for _, raw := range form.Value["image_urls"] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		attachments = append(attachments, AttachmentInput{URL: raw})
	}
	return content, taskID, attachments, cleanup, nil
}

// firstFormValue returns the first non-empty value in values[key], or "".
func firstFormValue(values map[string][]string, key string) string {
	if vals, ok := values[key]; ok {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// extractAttachmentIndex pulls the "attachment index N" suffix injected by
// ImageManager.Prepare or Service.CreateRequestWithImages out of a wrapped error, so the client can highlight
// the failing thumbnail. Returns -1 when the index is absent.
func extractAttachmentIndex(err error) int {
	if err == nil {
		return -1
	}
	message := err.Error()
	const marker = "attachment index "
	start := strings.LastIndex(message, marker)
	if start < 0 {
		return -1
	}
	rest := message[start+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 || (end < len(rest) && rest[end] != ':' && rest[end] != ' ') {
		return -1
	}
	index, parseErr := strconv.Atoi(rest[:end])
	if parseErr != nil {
		return -1
	}
	return index
}

// Image error codes of the HTTP contract (proposal §5.1). The web client
// mirrors them as ImageErrorCode.
const (
	imageErrInternal           = "internal_error"
	imageErrFeatureDisabled    = "feature_disabled"
	imageErrTooManyImages      = "too_many_images"
	imageErrQuotaExceeded      = "quota_exceeded"
	imageErrImageTooLarge      = "image_too_large"
	imageErrUnsupportedMIME    = "unsupported_mime"
	imageErrDecodeFailed       = "decode_failed"
	imageErrURLBlocked         = "url_blocked"
	imageErrURLTimeout         = "url_timeout"
	imageErrURLFetchFailed     = "url_fetch_failed"
	imageErrStorageUnavailable = "storage_unavailable"
	imageErrBusy               = "image_busy"
)

// imageErrorClass is the public side of an image error: its status, code
// and, for server-side failures, the stable message the client gets.
type imageErrorClass struct {
	status int
	code   string
}

// publicImageErrorMessages are the client messages of the server-side (5xx)
// image errors. A 5xx error chain carries internal details — object keys,
// storage operations, backend addresses, transport errors — that stay in the
// server log; the client gets the code, this message and the attachment
// index. Validated client-input errors (4xx) keep their descriptive message.
var publicImageErrorMessages = map[string]string{
	imageErrInternal:           "The image request failed because of a server error. Try again later.",
	imageErrStorageUnavailable: "Image storage is temporarily unavailable. Try again later.",
	imageErrBusy:               "The image service is busy. Retry after the Retry-After delay.",
	imageErrURLFetchFailed:     "The image URL could not be fetched.",
	imageErrURLTimeout:         "The image URL did not respond in time.",
}

// publicImageErrorMessage returns the client message of a server-side image
// error code (the internal_error message for an unknown code).
func publicImageErrorMessage(code string) string {
	if message, ok := publicImageErrorMessages[code]; ok {
		return message
	}
	return publicImageErrorMessages[imageErrInternal]
}

// classifyImageError maps an image pipeline error onto its status and code.
func classifyImageError(err error) imageErrorClass {
	switch {
	case errors.Is(err, ErrImageFeatureDisabled):
		return imageErrorClass{http.StatusUnsupportedMediaType, imageErrFeatureDisabled}
	case errors.Is(err, ErrTooManyImages):
		return imageErrorClass{http.StatusRequestEntityTooLarge, imageErrTooManyImages}
	case errors.Is(err, ErrQuotaExceeded):
		return imageErrorClass{http.StatusRequestEntityTooLarge, imageErrQuotaExceeded}
	case errors.Is(err, imageproc.ErrImageTooLarge):
		return imageErrorClass{http.StatusRequestEntityTooLarge, imageErrImageTooLarge}
	case errors.Is(err, imageproc.ErrUnsupportedMIME):
		return imageErrorClass{http.StatusBadRequest, imageErrUnsupportedMIME}
	case errors.Is(err, imageproc.ErrDimensionsTooLarge), errors.Is(err, imageproc.ErrDecodeFailed):
		return imageErrorClass{http.StatusUnprocessableEntity, imageErrDecodeFailed}
	case errors.Is(err, imageproc.ErrURLBlocked):
		return imageErrorClass{http.StatusBadRequest, imageErrURLBlocked}
	case errors.Is(err, imageproc.ErrURLTimeout):
		return imageErrorClass{http.StatusGatewayTimeout, imageErrURLTimeout}
	case errors.Is(err, imageproc.ErrURLFetchFailed):
		return imageErrorClass{http.StatusBadGateway, imageErrURLFetchFailed}
	case errors.Is(err, ErrStorageUnavailable):
		return imageErrorClass{http.StatusServiceUnavailable, imageErrStorageUnavailable}
	case errors.Is(err, imageproc.ErrDecodeBusy), errors.Is(err, ErrImageBusy):
		return imageErrorClass{http.StatusServiceUnavailable, imageErrBusy}
	default:
		return imageErrorClass{http.StatusInternalServerError, imageErrInternal}
	}
}

// writeImageError maps an image pipeline error onto an HTTP status + payload.
// The full error is logged; a 5xx response carries only the stable message
// of its code (publicImageErrorMessage).
func (h *httpHandler) writeImageError(w http.ResponseWriter, logger logSDK.Logger, err error, attachmentIndex int) {
	class := classifyImageError(err)
	message := err.Error()
	if class.status >= http.StatusInternalServerError {
		logger.Error("image create error", zap.Error(err), zap.String("code", class.code), zap.Int("attachment_index", attachmentIndex))
		message = publicImageErrorMessage(class.code)
	} else {
		logger.Warn("image create rejected", zap.String("code", class.code), zap.Error(err))
	}
	if class.code == imageErrBusy {
		w.Header().Set("Retry-After", "5")
	}

	payload := map[string]any{
		fieldError: class.code,
		"message":  message,
	}
	if attachmentIndex >= 0 {
		payload["attachment_index"] = attachmentIndex
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(class.status)
	_ = json.NewEncoder(w).Encode(payload) //nolint:errchkjson // best-effort error response
}

// serializeRequestWithPresign enriches a serialized request with presigned URLs for each image.
func serializeRequestWithPresign(ctx context.Context, req Request, manager *ImageManager, logger logSDK.Logger) map[string]any {
	payload := serializeRequest(req)
	if len(req.Images) == 0 {
		return payload
	}
	images := make([]map[string]any, 0, len(req.Images))
	for _, img := range req.Images {
		item := map[string]any{
			"id":           img.ID.String(),
			"sha256":       img.SHA256,
			"mime":         img.MIMEType,
			"size":         img.SizeBytes,
			"width":        img.Width,
			"height":       img.Height,
			fieldExpiresAt: img.ExpiresAt,
			"sort_order":   img.SortOrder,
		}
		if img.SourceURL != "" {
			item["source_url"] = img.SourceURL
		}
		if manager != nil {
			url, err := manager.PresignURL(ctx, img)
			if err != nil {
				logger.Warn("presign image", zap.String("image_id", img.ID.String()), zap.Error(err))
			} else {
				item["url"] = url
			}
		}
		images = append(images, item)
	}
	payload["images"] = images
	return payload
}

// serializeRequestsWithPresign is the list-API counterpart of
// serializeRequestWithPresign; it preserves the existing shape and only adds
// image metadata when present.
func serializeRequestsWithPresign(ctx context.Context, input []Request, manager *ImageManager, logger logSDK.Logger) []map[string]any {
	items := make([]map[string]any, 0, len(input))
	for _, req := range input {
		items = append(items, serializeRequestWithPresign(ctx, req, manager, logger))
	}
	return items
}

// parseBoundedMultipart caps total request bytes before form parsing.
func parseBoundedMultipart(r *http.Request, limit int64) error {
	if err := r.Context().Err(); err != nil {
		return errors.WithStack(err)
	}
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	reader, err := r.MultipartReader()
	if err != nil {
		return errors.Wrap(imageproc.ErrDecodeFailed, "invalid multipart content type")
	}
	form, err := reader.ReadForm(maxMultipartMemory)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) || r.Context().Err() != nil {
			// The body did not arrive before the request deadline.
			return errors.Wrap(ErrImageBusy, "request body not received before the deadline: "+err.Error())
		}
		return errors.Wrap(imageproc.ErrImageTooLarge, err.Error())
	}
	r.MultipartForm = form
	return nil
}
