package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// NewHTTPHandler constructs an HTTP mux exposing the file_io management APIs.
func NewHTTPHandler(service *Service, logger logSDK.Logger) http.Handler {
	return mcpauth.HTTPMiddleware(&filesHTTPHandler{service: service, logger: logger})
}

type filesHTTPHandler struct {
	service *Service
	logger  logSDK.Logger
}

const (
	versionsAPIPath = "/api/versions"
	fileAPIPath     = "/api/file"
)

// ServeHTTP routes requests for the file_io management endpoints.
func (h *filesHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == versionsAPIPath && r.Method == http.MethodGet:
		h.handleListVersions(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/versions/") && strings.HasSuffix(r.URL.Path, "/content") && r.Method == http.MethodGet:
		h.handleReadVersion(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/versions/") && strings.HasSuffix(r.URL.Path, "/restore") && r.Method == http.MethodPost:
		h.handleRestoreVersion(w, r)
	case r.URL.Path == fileAPIPath && r.Method == http.MethodPut:
		h.handlePutFile(w, r)
	default:
		logger := h.logFromCtx(r.Context())
		h.writeErrorWithLogger(w, logger, http.StatusNotFound, "resource not found")
	}
}

// handleListVersions returns version metadata for a given path.
func (h *filesHTTPHandler) handleListVersions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	if h.service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "files service unavailable")
		return
	}

	authCtx, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	query := r.URL.Query()
	project := query.Get("project")
	path := query.Get("path")

	versions, err := h.service.ListVersions(ctx, toFilesAuth(authCtx), project, path)
	if err != nil {
		h.writeFileError(w, logger, err, "list file versions")
		return
	}

	items := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		items = append(items, map[string]any{
			"id":         v.ID,
			"size":       v.Size,
			"created_at": v.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	h.writeJSON(w, map[string]any{"versions": items})
}

// handleReadVersion returns the content of a specific version.
func (h *filesHTTPHandler) handleReadVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	if h.service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "files service unavailable")
		return
	}

	versionID, err := parseVersionID(r.URL.Path, "/content")
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, err.Error())
		return
	}

	authCtx, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	query := r.URL.Query()
	project := query.Get("project")
	path := query.Get("path")

	version, err := h.service.ReadVersion(ctx, toFilesAuth(authCtx), project, path, versionID)
	if err != nil {
		h.writeFileError(w, logger, err, "read file version")
		return
	}

	encoding := "utf-8"
	var content string
	if utf8.Valid(version.Content) {
		content = string(version.Content)
	} else {
		encoding = "base64"
		content = base64.StdEncoding.EncodeToString(version.Content)
	}

	h.writeJSON(w, map[string]any{
		"content":          content,
		"content_encoding": encoding,
		"size":             version.Size,
		"created_at":       version.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

// handleRestoreVersion restores a version as the current file content.
func (h *filesHTTPHandler) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	if h.service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "files service unavailable")
		return
	}

	versionID, err := parseVersionID(r.URL.Path, "/restore")
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, err.Error())
		return
	}

	authCtx, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	var payload struct {
		Project string `json:"project"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	result, err := h.service.RestoreVersion(ctx, toFilesAuth(authCtx), payload.Project, payload.Path, versionID)
	if err != nil {
		h.writeFileError(w, logger, err, "restore file version")
		return
	}

	h.writeJSON(w, map[string]any{"bytes_written": result.BytesWritten})
}

// handlePutFile saves edited content using TRUNCATE write mode.
func (h *filesHTTPHandler) handlePutFile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	if h.service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "files service unavailable")
		return
	}

	authCtx, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	var payload struct {
		Project string `json:"project"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	result, err := h.service.Write(ctx, toFilesAuth(authCtx), payload.Project, payload.Path, payload.Content, "utf-8", 0, WriteModeTruncate)
	if err != nil {
		h.writeFileError(w, logger, err, "write file")
		return
	}

	h.writeJSON(w, map[string]any{"bytes_written": result.BytesWritten})
}

// parseVersionID extracts the numeric version ID from the URL path.
func parseVersionID(urlPath, suffix string) (uint64, error) {
	trimmed := strings.TrimPrefix(urlPath, "/api/versions/")
	trimmed = strings.TrimSuffix(trimmed, suffix)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return 0, errors.New("missing version id")
	}
	id, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, errors.Wrap(err, "invalid version id")
	}
	return id, nil
}

// toFilesAuth maps the shared auth context to the files-package AuthContext.
func toFilesAuth(authCtx *askuser.AuthorizationContext) AuthContext {
	if authCtx == nil {
		return AuthContext{}
	}
	return AuthContext{
		APIKey:       authCtx.APIKey,
		APIKeyHash:   authCtx.APIKeyHash,
		UserID:       authCtx.UserID,
		UserIdentity: authCtx.UserIdentity,
	}
}

// writeFileError converts a service error to an HTTP response.
func (h *filesHTTPHandler) writeFileError(w http.ResponseWriter, logger logSDK.Logger, err error, action string) {
	if typed, ok := AsError(err); ok {
		status := http.StatusInternalServerError
		switch typed.Code {
		case ErrCodeNotFound:
			status = http.StatusNotFound
		case ErrCodePermissionDenied:
			status = http.StatusUnauthorized
		case ErrCodeAlreadyExists, ErrCodeNotEmpty:
			status = http.StatusConflict
		case ErrCodeIsDirectory, ErrCodeNotDirectory, ErrCodeInvalidPath, ErrCodeInvalidOffset, ErrCodeInvalidQuery:
			status = http.StatusBadRequest
		case ErrCodePayloadTooLarge:
			status = http.StatusRequestEntityTooLarge
		case ErrCodeQuotaExceeded:
			status = http.StatusInsufficientStorage
		case ErrCodeRateLimited:
			status = http.StatusTooManyRequests
		case ErrCodeResourceBusy:
			status = http.StatusConflict
		}
		h.writeErrorWithLogger(w, logger, status, typed.Message)
		return
	}
	logger.Error(action, zap.Error(err))
	h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "internal server error")
}

// writeErrorWithLogger writes an error response with the provided logger.
func (h *filesHTTPHandler) writeErrorWithLogger(w http.ResponseWriter, logger logSDK.Logger, status int, message string) {
	if status >= 500 {
		logger.Error("files http error", zap.Int("status", status), zap.String("message", message))
	} else {
		logger.Warn("files http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message}) //nolint:errchkjson // best-effort error response
}

// writeJSON writes a JSON response body.
func (h *filesHTTPHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload) //nolint:errchkjson // best-effort JSON response
}

// logFromCtx returns a context-aware logger for the handler.
func (h *filesHTTPHandler) logFromCtx(ctx context.Context) logSDK.Logger {
	if logger := gmw.GetLogger(ctx); logger != nil {
		return logger.Named("files_http")
	}
	if h != nil && h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("files_http")
}
