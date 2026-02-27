package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	ginModeOnce sync.Once
)

func setupGinTestMode() {
	ginModeOnce.Do(func() {
		gin.SetMode(gin.TestMode)
	})
}

func TestAllowCORS(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		origin         string
		expectedStatus int
		expectedCORS   bool
		expectedOrigin string
	}{
		{
			name:           "No origin header - should pass through",
			method:         "GET",
			origin:         "",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "Valid subdomain origin - GET request",
			method:         "GET",
			origin:         "https://blog.laisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://blog.laisky.com",
		},
		{
			name:           "Valid subdomain origin - POST request",
			method:         "POST",
			origin:         "https://app.laisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://app.laisky.com",
		},
		{
			name:           "Valid main domain origin",
			method:         "GET",
			origin:         "https://laisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://laisky.com",
		},
		{
			name:           "Valid subdomain origin - OPTIONS preflight",
			method:         "OPTIONS",
			origin:         "https://blog.laisky.com",
			expectedStatus: http.StatusNoContent,
			expectedCORS:   true,
			expectedOrigin: "https://blog.laisky.com",
		},
		{
			name:           "Invalid origin - OPTIONS preflight",
			method:         "OPTIONS",
			origin:         "https://evil.com",
			expectedStatus: http.StatusForbidden,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "Invalid origin - GET request",
			method:         "GET",
			origin:         "https://evil.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "Invalid subdomain of different domain",
			method:         "GET",
			origin:         "https://laisky.com.evil.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "Case insensitive domain matching",
			method:         "GET",
			origin:         "https://Blog.LAISKY.COM",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://Blog.LAISKY.COM",
		},
		{
			name:           "Invalid origin with malformed URL",
			method:         "GET",
			origin:         "not-a-valid-url",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "HTTP origin (non-HTTPS)",
			method:         "GET",
			origin:         "http://blog.laisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "http://blog.laisky.com",
		},
		{
			name:           "Domain that contains laisky.com but is not subdomain",
			method:         "GET",
			origin:         "https://notlaisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
		{
			name:           "Multiple level subdomain",
			method:         "GET",
			origin:         "https://api.v2.laisky.com",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://api.v2.laisky.com",
		},
		{
			name:           "Valid CGNAT IP origin",
			method:         "GET",
			origin:         "https://100.70.1.2",
			expectedStatus: http.StatusOK,
			expectedCORS:   true,
			expectedOrigin: "https://100.70.1.2",
		},
		{
			name:           "Outside CGNAT range",
			method:         "GET",
			origin:         "https://100.128.0.1",
			expectedStatus: http.StatusOK,
			expectedCORS:   false,
			expectedOrigin: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a new gin router for each test
			router := gin.New()
			router.Use(allowCORS)

			// Add a test endpoint
			router.Any("/test", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"message": "success"})
			})

			// Create request
			req := httptest.NewRequest(tt.method, "/test", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Perform request
			router.ServeHTTP(w, req)

			// Assert status code
			assert.Equal(t, tt.expectedStatus, w.Code, "Status code mismatch")

			// Assert CORS headers
			if tt.expectedCORS {
				assert.Equal(t, tt.expectedOrigin, w.Header().Get("Access-Control-Allow-Origin"), "CORS origin header mismatch")
				assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"), "CORS credentials header mismatch")
				assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS, HEAD", w.Header().Get("Access-Control-Allow-Methods"), "CORS methods header mismatch")
				assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Headers"), "CORS headers mismatch")
				assert.Equal(t, "86400", w.Header().Get("Access-Control-Max-Age"), "CORS max age header mismatch")
				assert.Equal(t, "Origin", w.Header().Get("Vary"), "Vary header mismatch")
			} else {
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"), "CORS origin header should be empty")
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"), "CORS credentials header should be empty")
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"), "CORS methods header should be empty")
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"), "CORS headers should be empty")
				assert.Empty(t, w.Header().Get("Access-Control-Max-Age"), "CORS max age header should be empty")
			}
		})
	}
}

func TestAllowCORSEdgeCases(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	t.Run("Empty origin with OPTIONS method", func(t *testing.T) {
		t.Parallel()

		router := gin.New()
		router.Use(allowCORS)
		router.Any("/test", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "success"})
		})

		req := httptest.NewRequest("OPTIONS", "/test", nil)
		// No Origin header set

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Middleware should respond with generic CORS headers for blank origin preflight
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Headers"))
		assert.Equal(t, "GET, POST, PUT, DELETE, OPTIONS, HEAD", w.Header().Get("Access-Control-Allow-Methods"))
		assert.Equal(t, "86400", w.Header().Get("Access-Control-Max-Age"))
	})

	t.Run("Origin header with only spaces", func(t *testing.T) {
		t.Parallel()

		router := gin.New()
		router.Use(allowCORS)
		router.Any("/test", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "success"})
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "   ")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("Origin with port number", func(t *testing.T) {
		t.Parallel()

		router := gin.New()
		router.Use(allowCORS)
		router.Any("/test", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "success"})
		})

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Origin", "https://blog.laisky.com:8080")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "https://blog.laisky.com:8080", w.Header().Get("Access-Control-Allow-Origin"))
	})
}

func TestNewStatusHandler(t *testing.T) {
	setupGinTestMode()
	t.Parallel()

	handler := newStatusHandler()
	router := gin.New()
	router.GET("/status", handler)
	router.HEAD("/status", handler)
	router.OPTIONS("/status", handler)

	t.Run("GET returns ok", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "ok", strings.TrimSpace(w.Body.String()))
		assert.Equal(t, "GET, HEAD, OPTIONS", w.Header().Get("Allow"))
	})

	t.Run("HEAD returns 200", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodHead, "/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Body.String())
		assert.Equal(t, "GET, HEAD, OPTIONS", w.Header().Get("Allow"))
	})

	t.Run("OPTIONS returns 200", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodOptions, "/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Body.String())
		assert.Equal(t, "GET, HEAD, OPTIONS", w.Header().Get("Allow"))
	})
}

func TestClassifyGraphQLClientError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		errMsg         string
		expectedClient bool
		expectedReason string
	}{
		{
			name:           "alert token invalidation is client-side",
			errMsg:         "token invalidate for type monitor",
			expectedClient: true,
			expectedReason: "invalid_alert_token",
		},
		{
			name:           "throttle denial is client-side",
			errMsg:         "deny by throttle",
			expectedClient: true,
			expectedReason: "throttled_request",
		},
		{
			name:           "missing post by name is client-side",
			errMsg:         "cannot find post by name `history`",
			expectedClient: true,
			expectedReason: "post_not_found",
		},
		{
			name:           "generic not found is client-side",
			errMsg:         "resource not found",
			expectedClient: true,
			expectedReason: "resource_not_found",
		},
		{
			name:           "database failure is server-side",
			errMsg:         "dial tcp: connection refused",
			expectedClient: false,
			expectedReason: "server_error",
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			actualClient, actualReason := classifyGraphQLClientError(tc.errMsg)
			require.Equal(t, tc.expectedClient, actualClient)
			require.Equal(t, tc.expectedReason, actualReason)
		})
	}
}
