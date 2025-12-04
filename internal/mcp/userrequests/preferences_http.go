package userrequests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// preferencesHTTPHandler handles HTTP requests for user preferences.
type preferencesHTTPHandler struct {
	service *Service
	logger  logSDK.Logger
}

// ServeHTTP routes preferences requests based on method.
func (h *preferencesHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPut, http.MethodPost:
		h.handleSet(w, r)
	default:
		logger := h.logFromCtx(r.Context())
		h.writeErrorWithLogger(w, logger, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGet retrieves the user's preferences.
func (h *preferencesHTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "preferences service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	pref, err := service.GetUserPreference(ctx, auth)
	if err != nil {
		logger.Error("get user preferences", zap.Error(err))
		h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to get preferences")
		return
	}

	// Return defaults if no preference exists
	returnMode := DefaultReturnMode
	if pref != nil && pref.Preferences.ReturnMode != "" {
		returnMode = pref.Preferences.ReturnMode
	}

	h.writeJSON(w, map[string]any{
		"return_mode": returnMode,
		"user_id":     auth.UserIdentity,
		"key_hint":    auth.KeySuffix,
	})
}

// handleSet updates the user's preferences.
func (h *preferencesHTTPHandler) handleSet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logger := h.logFromCtx(ctx)

	service := h.service
	if service == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "preferences service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		ReturnMode string `json:"return_mode"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	// Validate return_mode
	if payload.ReturnMode != ReturnModeAll && payload.ReturnMode != ReturnModeFirst {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid return_mode: must be 'all' or 'first'")
		return
	}

	pref, err := service.SetReturnMode(ctx, auth, payload.ReturnMode)
	if err != nil {
		logger.Error("set user preferences", zap.Error(err))
		h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to save preferences")
		return
	}

	logger.Debug("user preferences updated",
		zap.String("user", auth.UserIdentity),
		zap.String("return_mode", pref.Preferences.ReturnMode),
	)

	h.writeJSON(w, map[string]any{
		"return_mode": pref.Preferences.ReturnMode,
		"user_id":     auth.UserIdentity,
		"key_hint":    auth.KeySuffix,
	})
}

// writeErrorWithLogger writes an error response with the provided logger for context-aware logging.
func (h *preferencesHTTPHandler) writeErrorWithLogger(w http.ResponseWriter, logger logSDK.Logger, status int, message string) {
	if status >= 500 {
		logger.Error("preferences http error", zap.Int("status", status), zap.String("message", message))
	} else {
		logger.Warn("preferences http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func (h *preferencesHTTPHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// logFromCtx extracts a context-aware logger from the context.
// Falls back to the handler's logger or a shared logger if context logger is unavailable.
func (h *preferencesHTTPHandler) logFromCtx(ctx context.Context) logSDK.Logger {
	if logger := gmw.GetLogger(ctx); logger != nil {
		return logger.Named("preferences_http")
	}
	if h != nil && h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("preferences_http")
}
