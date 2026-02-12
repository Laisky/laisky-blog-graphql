package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// withToolsListFiltering removes user-disabled tools from MCP tools/list responses.
func withToolsListFiltering(next http.Handler, logger logSDK.Logger, preferenceService *userrequests.Service) http.Handler {
	if next == nil {
		return nil
	}
	if preferenceService == nil {
		return next
	}

	sessionAuthStore := newSessionAuthorizationStore()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheSessionAuthorizationForRequest(r, logger, sessionAuthStore)
		shouldFilter, disabledTools := loadDisabledToolsForListRequest(r, preferenceService, logger, sessionAuthStore)
		if !shouldFilter || len(disabledTools) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		capture := newCaptureResponseWriter()
		next.ServeHTTP(capture, r)

		body := capture.body.Bytes()
		filtered, changed, err := filterToolsListBody(body, disabledTools)
		if err != nil {
			if logger != nil {
				logger.Warn("filter tools/list response failed", zap.Error(err))
			}
			writeCapturedResponse(w, capture, body)
			return
		}

		if !changed {
			writeCapturedResponse(w, capture, body)
			return
		}

		if logger != nil {
			logger.Debug("filtered tools/list response",
				zap.Int("disabled_tools", len(disabledTools)),
			)
		}

		writeCapturedResponse(w, capture, filtered)
	})
}

// loadDisabledToolsForListRequest inspects the request and returns disabled tools for tools/list calls.
func loadDisabledToolsForListRequest(r *http.Request, preferenceService *userrequests.Service, logger logSDK.Logger, sessionAuthStore *sessionAuthorizationStore) (bool, map[string]struct{}) {
	if r == nil || preferenceService == nil {
		return false, nil
	}
	if r.Method != http.MethodPost {
		return false, nil
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		if logger != nil {
			logger.Warn("read request body for tools/list filtering", zap.Error(err))
		}
		return false, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var payload struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, nil
	}
	if payload.Method != string(mcp.MethodToolsList) {
		return false, nil
	}

	auth, authSource := resolveAuthorizationForListRequest(r, sessionAuthStore)
	if auth == nil {
		if logger != nil {
			logger.Debug("skip tools/list filtering: authorization unavailable",
				zap.Bool("has_session_header", strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID)) != ""),
			)
		}
		return true, nil
	}

	if logger != nil {
		logger.Debug("tools/list filtering authorization resolved",
			zap.String("auth_source", authSource),
			zap.String("user_identity", auth.UserIdentity),
		)
	}

	disabledTools, err := preferenceService.GetDisabledTools(r.Context(), auth)
	if err != nil {
		if logger != nil {
			logger.Warn("load disabled tools failed",
				zap.Error(err),
				zap.String("auth_source", authSource),
				zap.String("user_identity", auth.UserIdentity),
			)
		}
		return true, nil
	}

	if len(disabledTools) == 0 {
		if logger != nil {
			logger.Debug("tools/list filtering: no disabled tools",
				zap.String("auth_source", authSource),
				zap.String("user_identity", auth.UserIdentity),
			)
		}
		return true, map[string]struct{}{}
	}

	set := make(map[string]struct{}, len(disabledTools))
	for _, name := range disabledTools {
		set[name] = struct{}{}
	}

	if logger != nil {
		logger.Debug("tools/list filtering loaded disabled tools",
			zap.String("auth_source", authSource),
			zap.String("user_identity", auth.UserIdentity),
			zap.Int("disabled_tools_count", len(set)),
		)
	}

	return true, set
}

// sessionAuthorizationStore stores per-session, non-sensitive authorization metadata for tools/list filtering.
type sessionAuthorizationStore struct {
	values sync.Map
}

// cachedAuthorization stores only identity metadata needed to query preferences without persisting raw API keys.
type cachedAuthorization struct {
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
}

// newSessionAuthorizationStore creates a fresh in-memory authorization cache scoped to one HTTP handler.
func newSessionAuthorizationStore() *sessionAuthorizationStore {
	return &sessionAuthorizationStore{}
}

// Set stores authorization metadata for a given MCP session ID.
func (s *sessionAuthorizationStore) Set(sessionID string, auth *askuser.AuthorizationContext) {
	if s == nil || auth == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}

	s.values.Store(sid, cachedAuthorization{
		APIKeyHash:   auth.APIKeyHash,
		KeySuffix:    auth.KeySuffix,
		UserIdentity: auth.UserIdentity,
	})
}

// Get returns cached authorization metadata for a session ID.
func (s *sessionAuthorizationStore) Get(sessionID string) (*askuser.AuthorizationContext, bool) {
	if s == nil {
		return nil, false
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, false
	}

	value, ok := s.values.Load(sid)
	if !ok {
		return nil, false
	}

	cached, ok := value.(cachedAuthorization)
	if !ok {
		return nil, false
	}

	return &askuser.AuthorizationContext{
		APIKeyHash:   cached.APIKeyHash,
		KeySuffix:    cached.KeySuffix,
		UserIdentity: cached.UserIdentity,
	}, true
}

// cacheSessionAuthorizationForRequest saves request authorization metadata when both session and auth headers are present.
func cacheSessionAuthorizationForRequest(r *http.Request, logger logSDK.Logger, sessionAuthStore *sessionAuthorizationStore) {
	if r == nil || sessionAuthStore == nil {
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID))
	if sessionID == "" {
		return
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		return
	}

	sessionAuthStore.Set(sessionID, auth)
	if logger != nil {
		logger.Debug("cached authorization for mcp session",
			zap.String("session_id", sessionID),
			zap.String("user_identity", auth.UserIdentity),
		)
	}
}

// resolveAuthorizationForListRequest resolves authorization for tools/list from header first, then session cache.
func resolveAuthorizationForListRequest(r *http.Request, sessionAuthStore *sessionAuthorizationStore) (*askuser.AuthorizationContext, string) {
	if r == nil {
		return nil, "none"
	}

	auth, err := askuser.ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err == nil {
		return auth, "header"
	}

	sessionID := strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID))
	if sessionID == "" {
		return nil, "none"
	}

	if cachedAuth, ok := sessionAuthStore.Get(sessionID); ok {
		return cachedAuth, "session"
	}

	return nil, "none"
}

// filterToolsListBody removes disabled tool definitions from a JSON-RPC tools/list response body.
func filterToolsListBody(body []byte, disabledTools map[string]struct{}) ([]byte, bool, error) {
	if len(body) == 0 || len(disabledTools) == 0 {
		return body, false, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, errors.Wrap(err, "unmarshal tools/list response")
	}

	resultRaw, ok := payload["result"]
	if !ok {
		return body, false, nil
	}
	result, ok := resultRaw.(map[string]any)
	if !ok {
		return body, false, nil
	}

	toolsRaw, ok := result["tools"]
	if !ok {
		return body, false, nil
	}
	toolsAny, ok := toolsRaw.([]any)
	if !ok {
		return body, false, nil
	}

	filteredTools := make([]any, 0, len(toolsAny))
	changed := false
	for _, candidate := range toolsAny {
		tool, ok := candidate.(map[string]any)
		if !ok {
			filteredTools = append(filteredTools, candidate)
			continue
		}

		name, _ := tool["name"].(string)
		if _, disabled := disabledTools[name]; disabled {
			changed = true
			continue
		}
		filteredTools = append(filteredTools, tool)
	}

	if !changed {
		return body, false, nil
	}

	result["tools"] = filteredTools
	payload["result"] = result
	filteredBody, err := json.Marshal(payload)
	if err != nil {
		return nil, false, errors.Wrap(err, "marshal filtered tools/list response")
	}

	return filteredBody, true, nil
}

// captureResponseWriter buffers downstream HTTP responses for post-processing.
type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

// newCaptureResponseWriter creates a buffered response writer.
func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{header: make(http.Header)}
}

// Header returns writable response headers.
func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

// Write stores response body bytes.
func (w *captureResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

// WriteHeader stores response status code.
func (w *captureResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

// writeCapturedResponse writes a buffered response to the real writer.
func writeCapturedResponse(dst http.ResponseWriter, src *captureResponseWriter, body []byte) {
	if dst == nil || src == nil {
		return
	}

	copyHeaders(dst.Header(), src.header)
	dst.Header().Del("Content-Length")

	status := src.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(body)
}

// copyHeaders clones HTTP header values from src into dst.
func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		copied := append([]string(nil), values...)
		dst[key] = copied
	}
}
