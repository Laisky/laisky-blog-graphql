package mcp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"
)

func newMCPHooks(logger logSDK.Logger) *srv.Hooks {
	if logger == nil {
		return nil
	}

	hooks := &srv.Hooks{}

	hooks.AddBeforeAny(func(ctx context.Context, id any, method mcp.MCPMethod, message any) {
		fields := hookLogFields(ctx, id, method)
		if message != nil {
			fields = append(fields, zap.String("request", redactHookPayload(message)))
		}
		logger.Debug("mcp request received", fields...)
	})

	hooks.AddOnSuccess(func(ctx context.Context, id any, method mcp.MCPMethod, message any, result any) {
		fields := hookLogFields(ctx, id, method)
		if result != nil {
			fields = append(fields, zap.String("response", redactHookPayload(result)))
		}
		logger.Info("mcp request succeeded", fields...)
	})

	hooks.AddOnError(func(ctx context.Context, id any, method mcp.MCPMethod, message any, err error) {
		fields := hookLogFields(ctx, id, method)
		if message != nil {
			fields = append(fields, zap.String("request", redactHookPayload(message)))
		}
		fields = append(fields, zap.Error(err))
		if shouldDowngradeMCPErrorLog(method, err) {
			logger.Debug("mcp request failed (non-critical)", fields...)
			return
		}
		logger.Error("mcp request failed", fields...)
	})

	hooks.AddOnRegisterSession(func(ctx context.Context, session srv.ClientSession) {
		logger.Info("mcp session registered", zap.String("session_id", session.SessionID()))
	})

	hooks.AddOnUnregisterSession(func(ctx context.Context, session srv.ClientSession) {
		logger.Info("mcp session unregistered", zap.String("session_id", session.SessionID()))
	})

	return hooks
}

// shouldDowngradeMCPErrorLog reports whether a request failure is an expected, non-critical MCP probe.
func shouldDowngradeMCPErrorLog(method mcp.MCPMethod, err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	if !strings.Contains(errText, "resources not supported") {
		return false
	}
	switch method {
	case mcp.MethodResourcesList, mcp.MethodResourcesTemplatesList:
		return true
	default:
		return false
	}
}

func hookLogFields(ctx context.Context, id any, method mcp.MCPMethod) []zap.Field {
	fields := []zap.Field{
		zap.Any("request_id", id),
		zap.String("method", string(method)),
	}

	if session := srv.ClientSessionFromContext(ctx); session != nil {
		fields = append(fields, zap.String("session_id", session.SessionID()))
	}

	return fields
}

func withHTTPLogging(next http.Handler, logger logSDK.Logger) http.Handler {
	if next == nil {
		return nil
	}
	if logger == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startAt := time.Now()
		body, truncated, err := readAndRestoreRequestBody(r, httpLogBodyLimit)
		if err != nil {
			logger.Error("read request body", zap.Error(err))
		}
		sessionID := strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID))

		redactedBody := redactMCPBody(body)
		logger.Debug("incoming http request",
			zap.String("method", r.Method),
			zap.String("url", r.URL.String()),
			zap.String("body", redactedBody),
			zap.Bool("body_truncated", truncated),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Bool("mcp_session_header_present", sessionID != ""),
			zap.String("mcp_session_id", sessionID),
		)

		lrw := newLoggingResponseWriter(w, httpLogBodyLimit)
		next.ServeHTTP(lrw, r)

		status := lrw.Status()
		respBody, respTruncated := lrw.Body()
		redactedResp := redactMCPBody(respBody)
		logger.Debug("outgoing http response",
			zap.String("method", r.Method),
			zap.String("url", r.URL.String()),
			zap.Int("status", status),
			zap.String("body", redactedResp),
			zap.Bool("body_truncated", respTruncated),
			zap.String("remote_addr", r.RemoteAddr),
			zap.Duration("cost", time.Since(startAt)),
		)
	})
}

func readAndRestoreRequestBody(r *http.Request, limit int) (string, bool, error) {
	if r.Body == nil {
		return "", false, nil
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return "", false, err
	}
	if err := r.Body.Close(); err != nil {
		return "", false, err
	}

	r.Body = io.NopCloser(bytes.NewReader(data))
	truncatedBody, truncated := truncateForLog(data, limit)
	return truncatedBody, truncated, nil
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status    int
	buffer    bytes.Buffer
	truncated bool
	bodyLimit int
}

func newLoggingResponseWriter(w http.ResponseWriter, limit int) *loggingResponseWriter {
	return &loggingResponseWriter{
		ResponseWriter: w,
		bodyLimit:      limit,
	}
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.status == 0 {
		lrw.status = http.StatusOK
	}

	if lrw.buffer.Len() < lrw.bodyLimit {
		remaining := lrw.bodyLimit - lrw.buffer.Len()
		if len(b) > remaining {
			lrw.buffer.Write(b[:remaining])
			lrw.truncated = true
		} else {
			lrw.buffer.Write(b)
		}
	} else {
		lrw.truncated = true
	}

	return lrw.ResponseWriter.Write(b)
}

func (lrw *loggingResponseWriter) Status() int {
	if lrw.status == 0 {
		return http.StatusOK
	}
	return lrw.status
}

func (lrw *loggingResponseWriter) Body() (string, bool) {
	return lrw.buffer.String(), lrw.truncated
}

func (lrw *loggingResponseWriter) Flush() {
	if flusher, ok := lrw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (lrw *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := lrw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("hijacker not supported")
}

func (lrw *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := lrw.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func truncateForLog(data []byte, limit int) (string, bool) {
	if len(data) <= limit {
		return string(data), false
	}
	return string(data[:limit]), true
}
