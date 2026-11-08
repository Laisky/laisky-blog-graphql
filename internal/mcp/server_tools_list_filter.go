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
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
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

// toolsListPeekLimit bounds how much of a request body the filter inspects. A
// tools/list request is tiny; anything larger is not filtered. Filtering is a
// presentation preference, not an access control, so skipping it is safe.
const toolsListPeekLimit = 64 << 10

// loadDisabledToolsForListRequest inspects the request and returns disabled tools for tools/list calls.
// It runs after the bearer gate and only for a verified caller, so neither an
// anonymous nor a rejected request causes a body read or a preference lookup.
func loadDisabledToolsForListRequest(r *http.Request, preferenceService *userrequests.Service, logger logSDK.Logger) (bool, map[string]struct{}) {
	if r == nil || preferenceService == nil {
		return false, nil
	}
	if r.Method != http.MethodPost {
		return false, nil
	}

	shared, verified := mcpauth.FromContext(r.Context())
	if !verified {
		if logger != nil {
			logger.Debug("skip tools/list filtering: no verified authorization",
				zap.Bool("has_session_header", strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID)) != ""),
			)
		}
		return false, nil
	}

	method, ok := peekJSONRPCMethod(r, toolsListPeekLimit)
	if !ok || method != string(mcp.MethodToolsList) {
		return false, nil
	}

	auth := askuser.FromSharedContext(shared)
	if logger != nil {
		logger.Debug("tools/list filtering authorization resolved",
			zap.String("user_identity", auth.UserIdentity),
		)
	}

	disabledTools, err := preferenceService.GetDisabledTools(r.Context(), auth)
	if err != nil {
		if logger != nil {
			logger.Warn("load disabled tools failed",
				zap.Error(err),
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
			zap.String("user_identity", auth.UserIdentity),
			zap.Int("disabled_tools_count", len(set)),
		)
	}

	return true, set
}

// peekJSONRPCMethod reads at most limit+1 bytes, which the transport body
// budget already bounds in size and time, decodes only the JSON-RPC method,
// and replays the inspected bytes (and any read error) to the SDK.
func peekJSONRPCMethod(r *http.Request, limit int) (string, bool) {
	if r.Body == nil || r.Body == http.NoBody {
		return "", false
	}
	source := r.Body
	prefix, err := io.ReadAll(io.LimitReader(source, int64(limit)+1))
	r.Body = &loggingReplayBody{ReadCloser: source, prefix: bytes.NewReader(prefix), readErr: err}
	if err != nil || len(prefix) > limit {
		return "", false
	}

	var payload struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(prefix, &payload); err != nil {
		return "", false
	}
	return payload.Method, true
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
