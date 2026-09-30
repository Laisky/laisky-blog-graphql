package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shouldFilter, disabledTools := loadDisabledToolsForListRequest(r, preferenceService, logger)
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
func loadDisabledToolsForListRequest(r *http.Request, preferenceService *userrequests.Service, logger logSDK.Logger) (bool, map[string]struct{}) {
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

	auth, authSource := resolveAuthorizationForListRequest(r)
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

// resolveAuthorizationForListRequest uses only the current request's credentials.
// Transport session IDs must never supply an identity or resurrect credentials.
func resolveAuthorizationForListRequest(r *http.Request) (*askuser.AuthorizationContext, string) {
	header, source := resolveRequestAuthorizationHeader(r)
	auth, err := askuser.ParseAuthorizationContext(header)
	if err != nil {
		return nil, authSourceNone
	}
	return auth, source
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
	setMCPResponseHeaders(dst.Header())

	status := src.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = writeMCPResponse(dst, body)
}

// copyHeaders clones HTTP header values from src into dst.
func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		copied := append([]string(nil), values...)
		dst[key] = copied
	}
}
