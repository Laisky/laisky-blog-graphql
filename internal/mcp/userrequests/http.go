package userrequests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// NewCombinedHTTPHandler creates a handler that routes both user requests and saved commands APIs.
// This avoids route conflicts in Gin by handling all paths under /api/* in a single handler.
// holdManager may be nil if the hold feature is not enabled.
func NewCombinedHTTPHandler(service *Service, holdManager *HoldManager, logger logSDK.Logger) http.Handler {
	handler := &combinedHTTPHandler{
		requestsHandler:     &httpHandler{service: service, holdManager: holdManager, logger: logger},
		savedCommandHandler: &savedCommandsHTTPHandler{service: service, logger: logger},
		preferencesHandler:  &preferencesHTTPHandler{service: service, logger: logger},
	}
	if holdManager != nil {
		handler.holdHandler = &holdHTTPHandler{holdManager: holdManager, logger: logger}
	}
	return handler
}

type combinedHTTPHandler struct {
	requestsHandler     *httpHandler
	savedCommandHandler *savedCommandsHTTPHandler
	holdHandler         *holdHTTPHandler
	preferencesHandler  *preferencesHTTPHandler
}

// ServeHTTP routes requests to the appropriate handler based on the URL path.
func (h *combinedHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/saved-commands"):
		h.savedCommandHandler.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/hold"):
		if h.holdHandler == nil {
			http.Error(w, "hold feature not enabled", http.StatusNotFound)
			return
		}
		h.holdHandler.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/preferences"):
		h.preferencesHandler.ServeHTTP(w, r)
	default:
		h.requestsHandler.ServeHTTP(w, r)
	}
}

// NewHTTPHandler constructs an HTTP mux exposing the user request APIs under /api/requests.
func NewHTTPHandler(service *Service, logger logSDK.Logger) http.Handler {
	return &httpHandler{service: service, logger: logger}
}

type httpHandler struct {
	service     *Service
	holdManager *HoldManager
	logger      logSDK.Logger
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/requests" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case r.URL.Path == "/api/requests" && r.Method == http.MethodPost:
		h.handleCreate(w, r)
	case r.URL.Path == "/api/requests" && r.Method == http.MethodDelete:
		h.handleDeleteAll(w, r)
	case r.URL.Path == "/api/requests/pending" && r.Method == http.MethodDelete:
		h.handleDeleteAllPending(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/requests/") && r.Method == http.MethodDelete:
		h.handleDeleteOne(w, r)
	default:
		h.writeError(w, http.StatusNotFound, "resource not found")
	}
}

func (h *httpHandler) handleList(w http.ResponseWriter, r *http.Request) {
	service := h.service
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "user requests service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	pending, consumed, err := service.ListRequests(ctx, auth)
	if err != nil {
		h.log().Error("list user requests", zap.Error(err), zap.String("api_key_hash", auth.APIKeyHash))
		h.writeError(w, http.StatusInternalServerError, "failed to load user requests")
		return
	}

	response := map[string]any{
		"pending":  serializeRequests(pending),
		"consumed": serializeRequests(consumed),
		"user_id":  auth.UserIdentity,
		"key_hint": auth.KeySuffix,
	}

	h.writeJSON(w, response)
}

func (h *httpHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	service := h.service
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "user requests service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		Content string `json:"content"`
		TaskID  string `json:"task_id"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := service.CreateRequest(ctx, auth, payload.Content, payload.TaskID)
	if err != nil {
		switch {
		case errors.Is(err, ErrEmptyContent):
			h.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeError(w, http.StatusUnauthorized, err.Error())
		default:
			h.log().Error("create user request", zap.Error(err))
			h.writeError(w, http.StatusInternalServerError, "failed to create user request")
		}
		return
	}

	// Notify hold manager if a hold is active for this user
	if h.holdManager != nil {
		h.holdManager.SubmitCommand(ctx, auth.APIKeyHash, req)
	}

	h.writeJSON(w, map[string]any{
		"request": serializeRequest(*req),
	})
}

func (h *httpHandler) handleDeleteOne(w http.ResponseWriter, r *http.Request) {
	service := h.service
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "user requests service unavailable")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/api/requests/")
	id, err := uuid.Parse(strings.TrimSpace(trimmed))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := service.DeleteRequest(ctx, auth, id); err != nil {
		switch {
		case errors.Is(err, ErrRequestNotFound):
			h.writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeError(w, http.StatusUnauthorized, err.Error())
		default:
			h.log().Error("delete user request", zap.Error(err))
			h.writeError(w, http.StatusInternalServerError, "failed to delete user request")
		}
		return
	}

	h.writeJSON(w, map[string]any{"deleted": true})
}

func (h *httpHandler) handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	service := h.service
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "user requests service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	deleted, err := service.DeleteAll(ctx, auth)
	if err != nil {
		h.log().Error("delete all user requests", zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, "failed to delete requests")
		return
	}

	h.writeJSON(w, map[string]any{"deleted": deleted})
}

func (h *httpHandler) handleDeleteAllPending(w http.ResponseWriter, r *http.Request) {
	service := h.service
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, "user requests service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	deleted, err := service.DeleteAllPending(ctx, auth)
	if err != nil {
		h.log().Error("delete pending user requests", zap.Error(err))
		h.writeError(w, http.StatusInternalServerError, "failed to delete pending requests")
		return
	}

	h.writeJSON(w, map[string]any{"deleted": deleted})
}

func (h *httpHandler) writeError(w http.ResponseWriter, status int, message string) {
	if status >= 500 {
		h.log().Error("user requests http error", zap.Int("status", status), zap.String("message", message))
	} else {
		h.log().Warn("user requests http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func (h *httpHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func (h *httpHandler) log() logSDK.Logger {
	if h != nil && h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("user_requests_http")
}

func serializeRequests(input []Request) []map[string]any {
	items := make([]map[string]any, 0, len(input))
	for _, req := range input {
		items = append(items, serializeRequest(req))
	}
	return items
}

func serializeRequest(req Request) map[string]any {
	payload := map[string]any{
		"id":            req.ID.String(),
		"content":       req.Content,
		"status":        req.Status,
		"task_id":       req.TaskID,
		"created_at":    req.CreatedAt,
		"updated_at":    req.UpdatedAt,
		"consumed_at":   req.ConsumedAt,
		"user_identity": req.UserIdentity,
	}
	return payload
}
