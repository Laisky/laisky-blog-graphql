package userrequests

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// holdHTTPHandler exposes HTTP endpoints for the hold feature.
type holdHTTPHandler struct {
	holdManager *HoldManager
	logger      logSDK.Logger
}

// NewHoldHTTPHandler constructs a handler for hold-related HTTP endpoints.
func NewHoldHTTPHandler(holdManager *HoldManager, logger logSDK.Logger) http.Handler {
	return &holdHTTPHandler{holdManager: holdManager, logger: logger}
}

// ServeHTTP routes hold requests based on path and method.
func (h *holdHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/hold" && r.Method == http.MethodGet:
		h.handleGetHold(w, r)
	case r.URL.Path == "/api/hold" && r.Method == http.MethodPost:
		h.handleSetHold(w, r)
	case r.URL.Path == "/api/hold" && r.Method == http.MethodDelete:
		h.handleReleaseHold(w, r)
	default:
		logger := h.logFromCtx(r.Context())
		h.writeErrorWithLogger(w, logger, http.StatusNotFound, "resource not found")
	}
}

// handleGetHold returns the current hold state for the authenticated user.
func (h *holdHTTPHandler) handleGetHold(w http.ResponseWriter, r *http.Request) {
	logger := h.logFromCtx(r.Context())

	if h.holdManager == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "hold service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	state := h.holdManager.GetHoldState(auth.APIKeyHash)
	h.writeJSON(w, map[string]any{
		"active":         state.Active,
		"waiting":        state.Waiting,
		"expires_at":     nullableTime(state.ExpiresAt),
		"remaining_secs": remainingSecs(state),
	})
}

// handleSetHold activates the hold for the authenticated user.
func (h *holdHTTPHandler) handleSetHold(w http.ResponseWriter, r *http.Request) {
	logger := h.logFromCtx(r.Context())

	if h.holdManager == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "hold service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	state := h.holdManager.SetHold(auth.APIKeyHash)
	logger.Info("hold activated via HTTP",
		zap.String("user", auth.UserIdentity),
	)

	h.writeJSON(w, map[string]any{
		"active":         state.Active,
		"waiting":        state.Waiting,
		"expires_at":     nullableTime(state.ExpiresAt),
		"remaining_secs": remainingSecs(state),
	})
}

// handleReleaseHold deactivates the hold for the authenticated user.
func (h *holdHTTPHandler) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	logger := h.logFromCtx(r.Context())

	if h.holdManager == nil {
		h.writeErrorWithLogger(w, logger, http.StatusServiceUnavailable, "hold service unavailable")
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		h.writeErrorWithLogger(w, logger, http.StatusUnauthorized, err.Error())
		return
	}

	h.holdManager.ReleaseHold(auth.APIKeyHash)
	logger.Info("hold released via HTTP", zap.String("user", auth.UserIdentity))

	h.writeJSON(w, map[string]any{
		"active":         false,
		"waiting":        false,
		"expires_at":     nil,
		"remaining_secs": 0,
	})
}

// writeErrorWithLogger writes an error response with the provided logger for context-aware logging.
func (h *holdHTTPHandler) writeErrorWithLogger(w http.ResponseWriter, logger logSDK.Logger, status int, message string) {
	if status >= 500 {
		logger.Error("hold http error", zap.Int("status", status), zap.String("message", message))
	} else {
		logger.Warn("hold http warning", zap.Int("status", status), zap.String("message", message))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func (h *holdHTTPHandler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// logFromCtx extracts a context-aware logger from the context.
// Falls back to the handler's logger or a shared logger if context logger is unavailable.
func (h *holdHTTPHandler) logFromCtx(ctx context.Context) logSDK.Logger {
	if logger := gmw.GetLogger(ctx); logger != nil {
		return logger.Named("hold_http")
	}
	if h != nil && h.logger != nil {
		return h.logger
	}
	return logSDK.Shared.Named("hold_http")
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func remainingSecs(state HoldState) float64 {
	if !state.Active || state.ExpiresAt.IsZero() {
		return 0
	}
	remaining := time.Until(state.ExpiresAt).Seconds()
	if remaining < 0 {
		return 0
	}
	return remaining
}
