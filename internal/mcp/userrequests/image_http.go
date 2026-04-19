package userrequests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
func (h *httpHandler) parseMultipart(r *http.Request) (string, string, []AttachmentInput, error) {
	var bodyCap int64 = 110 * 1024 * 1024
	if h.imageManager != nil {
		settings := h.imageManager.Settings()
		if settings.PerImageMaxBytes > 0 && settings.MaxPerRequest > 0 {
			bodyCap = settings.PerImageMaxBytes*int64(settings.MaxPerRequest) + (2 << 20)
		}
	}
	r.Body = http.MaxBytesReader(nil, r.Body, bodyCap)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		return "", "", nil, errors.Wrap(imageproc.ErrImageTooLarge, err.Error())
	}
	form := r.MultipartForm
	if form == nil {
		return "", "", nil, errors.New("multipart form missing")
	}

	content := strings.TrimSpace(firstFormValue(form.Value, "content"))
	taskID := strings.TrimSpace(firstFormValue(form.Value, "task_id"))

	var attachments []AttachmentInput
	for _, hdr := range form.File["images"] {
		if hdr == nil {
			continue
		}
		file, err := hdr.Open()
		if err != nil {
			return "", "", nil, errors.Wrap(err, "open multipart part")
		}
		body, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			return "", "", nil, errors.Wrap(readErr, "read multipart part")
		}
		attachments = append(attachments, AttachmentInput{
			FileBytes: body,
			Filename:  hdr.Filename,
		})
	}
	for _, raw := range form.Value["image_urls"] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		attachments = append(attachments, AttachmentInput{URL: raw})
	}
	return content, taskID, attachments, nil
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
// ImageManager.Process out of a wrapped error, so the client can highlight
// the failing thumbnail. Returns -1 when the index is absent.
func extractAttachmentIndex(err error) int {
	s := err.Error()
	marker := "attachment index "
	idx := strings.LastIndex(s, marker)
	if idx < 0 {
		return -1
	}
	rest := s[idx+len(marker):]
	var n int
	var parsed int
	_, scanErr := fmtSscanf(rest, "%d%n", &parsed, &n)
	_ = n
	if scanErr != nil {
		return -1
	}
	return parsed
}

// fmtSscanf is declared here so image_http.go does not need to pull in fmt at
// the top of http.go. Keeping it local also gives us the option to add extra
// parsing strictness later.
func fmtSscanf(s, format string, a ...any) (int, error) {
	return sscanf(s, format, a...)
}

// sscanf is a thin wrapper over fmt.Sscanf to decouple imports.
var sscanf = defaultSscanf

// writeImageError maps an image pipeline error onto an HTTP status + payload.
func (h *httpHandler) writeImageError(w http.ResponseWriter, logger logSDK.Logger, err error, attachmentIndex int) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, ErrImageFeatureDisabled):
		status = http.StatusUnsupportedMediaType
		code = "feature_disabled"
	case errors.Is(err, ErrTooManyImages):
		status = http.StatusRequestEntityTooLarge
		code = "too_many_images"
	case errors.Is(err, ErrQuotaExceeded):
		status = http.StatusRequestEntityTooLarge
		code = "quota_exceeded"
	case errors.Is(err, imageproc.ErrImageTooLarge):
		status = http.StatusRequestEntityTooLarge
		code = "image_too_large"
	case errors.Is(err, imageproc.ErrUnsupportedMIME):
		status = http.StatusBadRequest
		code = "unsupported_mime"
	case errors.Is(err, imageproc.ErrDimensionsTooLarge):
		status = http.StatusUnprocessableEntity
		code = "decode_failed"
	case errors.Is(err, imageproc.ErrDecodeFailed):
		status = http.StatusUnprocessableEntity
		code = "decode_failed"
	case errors.Is(err, imageproc.ErrURLBlocked):
		status = http.StatusBadRequest
		code = "url_blocked"
	case errors.Is(err, imageproc.ErrURLTimeout):
		status = http.StatusGatewayTimeout
		code = "url_timeout"
	case errors.Is(err, imageproc.ErrURLFetchFailed):
		status = http.StatusBadGateway
		code = "url_fetch_failed"
	case errors.Is(err, ErrStorageUnavailable):
		status = http.StatusServiceUnavailable
		code = "storage_unavailable"
	}

	if status >= 500 {
		logger.Error("image create error", zap.Error(err), zap.String("code", code))
	} else {
		logger.Warn("image create rejected", zap.String("code", code), zap.Error(err))
	}

	payload := map[string]any{
		"error":   code,
		"message": err.Error(),
	}
	if attachmentIndex >= 0 {
		payload["attachment_index"] = attachmentIndex
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
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
			"id":         img.ID.String(),
			"sha256":     img.SHA256,
			"mime":       img.MIMEType,
			"size":       img.SizeBytes,
			"width":      img.Width,
			"height":     img.Height,
			"expires_at": img.ExpiresAt,
			"sort_order": img.SortOrder,
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
