package userrequests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// NewSavedCommandsHTTPHandler constructs an HTTP mux exposing the saved commands APIs under /api/saved-commands.
func NewSavedCommandsHTTPHandler(service *Service, logger logSDK.Logger) http.Handler {
	return mcpauth.HTTPMiddleware(&savedCommandsHTTPHandler{service: service, logger: logger})
}

type savedCommandsHTTPHandler struct {
	service *Service
	logger  logSDK.Logger
}

// ServeHTTP routes requests for saved commands endpoints.
func (h *savedCommandsHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/saved-commands" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case r.URL.Path == "/api/saved-commands" && r.Method == http.MethodPost:
		h.handleCreate(w, r)
	case r.URL.Path == "/api/saved-commands/reorder" && r.Method == http.MethodPut:
		h.handleReorder(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/saved-commands/") && r.Method == http.MethodPut:
		h.handleUpdate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/saved-commands/") && r.Method == http.MethodDelete:
		h.handleDelete(w, r)
	default:
		logger := h.logFromCtx(r.Context())
		h.writeErrorWithLogger(w, logger, http.StatusNotFound, "resource not found")
	}
}

func (h *savedCommandsHTTPHandler) handleList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "saved commands service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	commands, err := service.ListSavedCommands(ctx, auth)
	if err != nil {
		logger.Error("list saved commands", zap.Error(err), zap.String("api_key_hash", auth.APIKeyHash))
		h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to load saved commands")
		return
	}

	dtos := make([]SavedCommandDTO, 0, len(commands))
	for _, cmd := range commands {
		dtos = append(dtos, cmd.ToDTO())
	}

	h.writeJSON(w, map[string]any{
		"commands": dtos,
		"user_id":  auth.UserIdentity,
		"key_hint": auth.KeySuffix,
	})
}

func (h *savedCommandsHTTPHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "saved commands service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		Label   string `json:"label"`
		Content string `json:"content"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	cmd, err := service.CreateSavedCommand(ctx, auth, payload.Label, payload.Content)
	if err != nil {
		switch {
		case errors.Is(err, ErrEmptyContent):
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrSavedCommandLimitReached):
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, err.Error())
		default:
			logger.Error("create saved command", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to create saved command")
		}
		return
	}

	h.writeJSON(w, map[string]any{
		"command": cmd.ToDTO(),
	})
}

func (h *savedCommandsHTTPHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "saved commands service unavailable")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/api/saved-commands/")
	id, err := uuid.Parse(strings.TrimSpace(trimmed))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid command id")
		return
	}

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		Label     *string `json:"label,omitempty"`
		Content   *string `json:"content,omitempty"`
		SortOrder *int    `json:"sort_order,omitempty"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	cmd, err := service.UpdateSavedCommand(ctx, auth, id, payload.Label, payload.Content, payload.SortOrder)
	if err != nil {
		switch {
		case errors.Is(err, ErrSavedCommandNotFound):
			h.writeErrorWithLogger(w, logger, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrEmptyContent):
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		default:
			logger.Error("update saved command", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to update saved command")
		}
		return
	}

	h.writeJSON(w, map[string]any{
		"command": cmd.ToDTO(),
	})
}

func (h *savedCommandsHTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "saved commands service unavailable")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/api/saved-commands/")
	id, err := uuid.Parse(strings.TrimSpace(trimmed))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid command id")
		return
	}

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	if err := service.DeleteSavedCommand(ctx, auth, id); err != nil {
		switch {
		case errors.Is(err, ErrSavedCommandNotFound):
			h.writeErrorWithLogger(w, logger, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		default:
			logger.Error("delete saved command", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to delete saved command")
		}
		return
	}

	h.writeJSON(w, map[string]any{"deleted": true})
}

func (h *savedCommandsHTTPHandler) handleReorder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "saved commands service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		OrderedIDs []string `json:"ordered_ids"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	orderedUUIDs := make([]uuid.UUID, 0, len(payload.OrderedIDs))
	for _, idStr := range payload.OrderedIDs {
		id, err := uuid.Parse(strings.TrimSpace(idStr))
		if err != nil {
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid command id in ordered_ids: "+idStr)
			return
		}
		orderedUUIDs = append(orderedUUIDs, id)
	}

	if err := service.ReorderSavedCommands(ctx, auth, orderedUUIDs); err != nil {
		switch {
		case errors.Is(err, ErrInvalidAuthorization):
			h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		default:
			logger.Error("reorder saved commands", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to reorder saved commands")
		}
		return
	}

	h.writeJSON(w, map[string]any{"success": true})
}

// writeErrorWithLogger writes an error response with the provided logger for context-aware logging.
func (h *savedCommandsHTTPHandler) writeErrorWithLogger(w http.ResponseWriter, logger logSDK.Logger, status int, message string) {
	if status >= 500 {
		logger.Error("saved commands http error", zap.Int("status", status), zap.String("message", message))
	} else {
		logger.Warn("saved commands http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func (h *savedCommandsHTTPHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// logFromCtx extracts a context-aware logger from the context.
// Falls back to the handler's logger or a shared logger if context logger is unavailable.
func (h *savedCommandsHTTPHandler) logFromCtx(ctx context.Context) logSDK.Logger {
	if logger := gmw.GetLogger(ctx); logger != nil {
		return logger.Named("saved_commands_http")
	}
	if h != nil && h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("saved_commands_http")
}
