package askuser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestAskUserConsoleValidatesBearer covers the ask_user console, which
// authenticates per handler rather than through the shared middleware.
func TestAskUserConsoleValidatesBearer(t *testing.T) {
	var calls atomic.Int64
	authtest.Swap(t, authtest.NewIssuedOnly(&calls, "sk-issued-ask-user"))
	handler := NewHTTPHandler(nil, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	request.Header.Set("Authorization", "Bearer sk-random-ask-user")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.Equal(t, int64(1), calls.Load())

	request = httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	request.Header.Set("Authorization", "Bearer sk-issued-ask-user")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, "a valid key passes the gate and reaches the (absent) service")

	outage, err := mcpauth.NewAuthenticator(mcpauth.ValidatorFunc(func(context.Context, string) error {
		return mcpauth.ErrValidationUnavailable
	}), mcpauth.AuthenticatorOptions{})
	require.NoError(t, err)
	authtest.Swap(t, outage)
	request = httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	request.Header.Set("Authorization", "Bearer sk-random-ask-user")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, "5", response.Header().Get("Retry-After"))
}
