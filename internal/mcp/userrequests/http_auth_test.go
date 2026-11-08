package userrequests

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// TestConsoleAPIRejectsUnissuedBearerWithoutStorage reproduces issue #86 on the
// console HTTP API: a random bearer must not create a tenant row.
func TestConsoleAPIRejectsUnissuedBearerWithoutStorage(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)
	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), func() []string { return nil })
	var authorityCalls atomic.Int64
	authtest.Swap(t, authtest.NewIssuedOnly(&authorityCalls, "sk-issued-console-key"))

	for _, path := range []string{"/api/requests", "/api/saved-commands", "/api/preferences"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"content":"plant a tenant","label":"x","return_mode":"first"}`))
		request.Header.Set("Authorization", "Bearer sk-random-console-tenant")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code, "%s: %s", path, response.Body.String())
		require.Contains(t, response.Header().Get("Content-Type"), "application/json")
	}

	for _, table := range []string{"mcp_user_requests", "mcp_saved_commands", "mcp_user_preferences"} {
		var rows int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&rows))
		require.Zero(t, rows, "an unvalidated bearer must not create %s rows", table)
	}
	require.Equal(t, int64(1), authorityCalls.Load(), "the rejection is cached, and the authority was really consulted")

	// The issued key keeps working through the same gate.
	request := httptest.NewRequest(http.MethodPost, "/api/requests", strings.NewReader(`{"content":"legitimate"}`))
	request.Header.Set("Authorization", "Bearer sk-issued-console-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

// TestConsoleAPIFailsClosedWhenAuthorityUnavailable keeps an unvalidated key
// out during an outage: 503 with Retry-After, never 401 and never a tenant.
func TestConsoleAPIFailsClosedWhenAuthorityUnavailable(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := NewService(db, nil, func() time.Time { return time.Now().UTC() }, Settings{RetentionDays: DefaultRetentionDays})
	require.NoError(t, err)
	handler := NewCombinedHTTPHandler(svc, nil, log.Logger.Named("test"), func() []string { return nil })
	outage, err := mcpauth.NewAuthenticator(mcpauth.ValidatorFunc(func(context.Context, string) error {
		return mcpauth.ErrValidationUnavailable
	}), mcpauth.AuthenticatorOptions{})
	require.NoError(t, err)
	authtest.Swap(t, outage)

	request := httptest.NewRequest(http.MethodPost, "/api/requests", strings.NewReader(`{"content":"during outage"}`))
	request.Header.Set("Authorization", "Bearer sk-never-validated")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.NotEmpty(t, response.Header().Get("Retry-After"))
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mcp_user_requests`).Scan(&rows))
	require.Zero(t, rows)
}
