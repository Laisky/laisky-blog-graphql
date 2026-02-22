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
	service                *Service
	logger                 logSDK.Logger
	availableToolsProvider func() []string
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

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
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
	disabledTools := []string{}
	if pref != nil && pref.Preferences.ReturnMode != "" {
		returnMode = pref.Preferences.ReturnMode
	}
	if pref != nil {
		disabledTools = NormalizeDisabledTools(pref.Preferences.DisabledTools)
	}

	availableTools := h.availableTools()

	logger.Debug("preferences GET response",
		zap.String("user", auth.UserIdentity),
		zap.Bool("pref_exists", pref != nil),
		zap.String("return_mode", returnMode),
		zap.Int("disabled_tools_count", len(disabledTools)),
		zap.Int("available_tools_count", len(availableTools)),
	)

	h.writeJSON(w, map[string]any{
		"return_mode":     returnMode,
		"disabled_tools":  disabledTools,
		"available_tools": availableTools,
		"user_id":         auth.UserIdentity,
		"key_hint":        auth.KeySuffix,
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

	auth, err := askuser.ParseAuthorizationFromContext(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	payload := struct {
		ReturnMode    *string  `json:"return_mode"`
		DisabledTools []string `json:"disabled_tools"`
	}{}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&payload); err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	logger.Debug("preferences SET request received",
		zap.String("user", auth.UserIdentity),
		zap.String("requested_mode", stringValue(payload.ReturnMode)),
		zap.Int("requested_disabled_tools", len(payload.DisabledTools)),
	)

	if payload.ReturnMode == nil && payload.DisabledTools == nil {
		h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "payload must include return_mode and/or disabled_tools")
		return
	}

	if payload.ReturnMode != nil {
		if *payload.ReturnMode != ReturnModeAll && *payload.ReturnMode != ReturnModeFirst {
			h.writeErrorWithLogger(w, logger, http.StatusBadRequest, "invalid return_mode: must be 'all' or 'first'")
			return
		}

		if _, err := service.SetReturnMode(ctx, auth, *payload.ReturnMode); err != nil {
			logger.Error("set return mode preference", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to save preferences")
			return
		}
	}

	if payload.DisabledTools != nil {
		if _, err := service.SetDisabledTools(ctx, auth, payload.DisabledTools); err != nil {
			logger.Error("set disabled tools preference", zap.Error(err))
			h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to save preferences")
			return
		}
	}

	pref, err := service.GetUserPreference(ctx, auth)
	if err != nil {
		logger.Error("reload user preferences", zap.Error(err))
		h.writeErrorWithLogger(w, logger, http.StatusInternalServerError, "failed to load preferences")
		return
	}

	returnMode := DefaultReturnMode
	disabledTools := []string{}
	if pref != nil && pref.Preferences.ReturnMode != "" {
		returnMode = pref.Preferences.ReturnMode
	}
	if pref != nil {
		disabledTools = NormalizeDisabledTools(pref.Preferences.DisabledTools)
	}
	availableTools := h.availableTools()

	logger.Debug("preferences SET succeeded",
		zap.String("user", auth.UserIdentity),
		zap.String("saved_mode", returnMode),
		zap.Int("saved_disabled_tools", len(disabledTools)),
	)

	h.writeJSON(w, map[string]any{
		"return_mode":     returnMode,
		"disabled_tools":  disabledTools,
		"available_tools": availableTools,
		"user_id":         auth.UserIdentity,
		"key_hint":        auth.KeySuffix,
	})
}

// availableTools returns the server-exposed MCP tool catalog in a stable format.
func (h *preferencesHTTPHandler) availableTools() []string {
	if h == nil || h.availableToolsProvider == nil {
		return []string{}
	}
	return NormalizeDisabledTools(h.availableToolsProvider())
}

// stringValue returns a pointer-backed string or an empty value when nil.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
