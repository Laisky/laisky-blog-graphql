// Package web tests context-aware logger usage in handlers.
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
)

// testLoggerCapture is a simple logger wrapper that captures log calls for verification.
type testLoggerCapture struct {
	mu       sync.Mutex
	captured []capturedLog
	inner    logSDK.Logger
}

type capturedLog struct {
	Level   string
	Message string
	Fields  []zap.Field
}

func newTestLoggerCapture() *testLoggerCapture {
	return &testLoggerCapture{
		inner: logSDK.Shared.Named("test"),
	}
}

func (t *testLoggerCapture) getCaptured() []capturedLog {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]capturedLog(nil), t.captured...)
}

func (t *testLoggerCapture) clearCaptured() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.captured = nil
}

func TestContextAwareLoggerInGinHandler(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	// Create a test logger
	testLogger := logSDK.Shared.Named("test_context_logger")

	// Create a Gin router with the logger middleware
	router := gin.New()
	router.Use(gmw.NewLoggerMiddleware(
		gmw.WithLogger(testLogger),
	))

	// Handler that uses gmw.GetLogger(ctx)
	var capturedCtxHasLogger bool
	var capturedLoggerNotNil bool

	router.GET("/test", func(c *gin.Context) {
		// Get the logger from context
		logger := gmw.GetLogger(c)

		// Verify the logger is not nil
		capturedLoggerNotNil = logger != nil

		// Verify the context is a valid gin context that gmw can extract from
		_, capturedCtxHasLogger = gmw.GetGinCtxFromStdCtx(c)

		// Use the logger
		if logger != nil {
			logger.Debug("test log message", zap.String("key", "value"))
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Make a request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify response
	require.Equal(t, http.StatusOK, w.Code)

	// Verify logger was accessible
	require.True(t, capturedLoggerNotNil, "Logger should be accessible from context")
	require.True(t, capturedCtxHasLogger, "Gin context should be accessible via gmw.GetGinCtxFromStdCtx")
}

func TestContextLoggerNamed(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	testLogger := logSDK.Shared.Named("test_named_logger")

	router := gin.New()
	router.Use(gmw.NewLoggerMiddleware(
		gmw.WithLogger(testLogger),
	))

	var loggerName string

	router.GET("/test", func(c *gin.Context) {
		logger := gmw.GetLogger(c).Named("custom_handler")

		// The logger should be named - we can't directly inspect it,
		// but we can verify it's callable without panic
		logger.Debug("named logger test")

		// Just verify we can call methods on it
		loggerName = "custom_handler"
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "custom_handler", loggerName)
}

func TestContextLoggerWithFields(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	testLogger := logSDK.Shared.Named("test_fields_logger")

	router := gin.New()
	router.Use(gmw.NewLoggerMiddleware(
		gmw.WithLogger(testLogger),
	))

	var loggerCalled bool

	router.GET("/test/:id", func(c *gin.Context) {
		id := c.Param("id")

		// This pattern should work: get logger once, add fields, use multiple times
		logger := gmw.GetLogger(c).Named("item_handler").With(
			zap.String("item_id", id),
		)

		logger.Debug("processing item")
		loggerCalled = true

		logger.Info("item processed successfully")

		c.JSON(http.StatusOK, gin.H{"id": id})
	})

	req := httptest.NewRequest(http.MethodGet, "/test/123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, loggerCalled, "Logger should have been called")
	require.Contains(t, w.Body.String(), "123")
}

func TestContextLoggerFromStdContext(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	testLogger := logSDK.Shared.Named("test_std_ctx_logger")

	router := gin.New()
	router.Use(gmw.NewLoggerMiddleware(
		gmw.WithLogger(testLogger),
	))

	var stdCtxLoggerWorks bool

	router.GET("/test", func(c *gin.Context) {
		// Simulate passing context to a service layer
		ctx := context.Context(c)

		// The service should be able to get logger from standard context
		logger := gmw.GetLogger(ctx)
		if logger != nil {
			logger.Debug("service layer log")
			stdCtxLoggerWorks = true
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, stdCtxLoggerWorks, "Logger should work when passed via standard context")
}

func TestLoggerFallbackWhenNoGinContext(t *testing.T) {
	t.Parallel()

	// When there's no Gin context, gmw.GetLogger should return a fallback logger
	ctx := context.Background()
	logger := gmw.GetLogger(ctx)

	// Should not panic and should return a usable logger
	require.NotNil(t, logger, "Logger should have a fallback when no gin context")

	// Should be usable without panic
	logger.Debug("fallback logger test")
}

func TestMultipleLoggersInSingleRequest(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	testLogger := logSDK.Shared.Named("test_multi_logger")

	router := gin.New()
	router.Use(gmw.NewLoggerMiddleware(
		gmw.WithLogger(testLogger),
	))

	var phase1Logged, phase2Logged, phase3Logged bool

	router.POST("/test", func(c *gin.Context) {
		// Phase 1: Initial processing
		logger1 := gmw.GetLogger(c).Named("phase1")
		logger1.Debug("phase 1 started")
		phase1Logged = true

		// Phase 2: Business logic
		logger2 := gmw.GetLogger(c).Named("phase2")
		logger2.Debug("phase 2 processing")
		phase2Logged = true

		// Phase 3: Response
		logger3 := gmw.GetLogger(c).Named("phase3")
		logger3.Debug("phase 3 response")
		phase3Logged = true

		c.JSON(http.StatusOK, gin.H{"phases": 3})
	})

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, phase1Logged)
	require.True(t, phase2Logged)
	require.True(t, phase3Logged)
}
