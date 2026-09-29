package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMCPReviewSessionManager rejects tampering, cross-credential use and anonymous deletion.
func TestMCPReviewSessionManager(t *testing.T) {
	t.Parallel()
	resolver := requestSessionIDResolver{}
	request := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	request.Header.Set("Authorization", reviewAlpha)
	owner := resolver.ResolveSessionIdManager(request)
	session := owner.Generate()
	require.NotEqual(t, session, owner.Generate())
	require.NotContains(t, session, "review-session-alpha")
	_, err := owner.Validate(session)
	require.NoError(t, err)
	_, err = owner.Terminate(session)
	require.NoError(t, err)

	otherRequest := request.Clone(request.Context())
	otherRequest.Header.Set("Authorization", reviewBeta)
	for _, manager := range []interface {
		Validate(string) (bool, error)
		Terminate(string) (bool, error)
	}{resolver.ResolveSessionIdManager(otherRequest), resolver.ResolveSessionIdManager(nil)} {
		_, err = manager.Validate(session)
		require.Error(t, err)
		_, err = manager.Terminate(session)
		require.Error(t, err)
	}

	nonce, mac, found := strings.Cut(session, ".")
	require.True(t, found)
	for _, bad := range []string{"", nonce, session + "x", nonce + "." + strings.Repeat("g", len(mac)),
		"mcp-session-invalid." + mac, nonce + "." + strings.Repeat("0", len(mac)),
		session + "." + mac, owner.Generate()[:len(nonce)] + "." + mac} {
		_, err = owner.Validate(bad)
		require.Error(t, err)
		_, err = owner.Terminate(bad)
		require.Error(t, err)
	}

	public := resolver.ResolveSessionIdManager(nil)
	publicID := public.Generate()
	_, err = public.Validate(publicID)
	require.NoError(t, err)
	_, err = public.Terminate(publicID)
	require.NoError(t, err)
	_, err = owner.Validate(publicID)
	require.Error(t, err, "anonymous sessions cannot be rebound to a credential")
}

// TestMCPReviewSessionCredentialForms preserves canonical query credentials and header precedence.
func TestMCPReviewSessionCredentialForms(t *testing.T) {
	for _, alias := range []string{"APIKEY", "apikey", "api_key"} {
		t.Run(alias, func(t *testing.T) {
			s := newReviewSessionServer(t)
			queryHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cloned := r.Clone(r.Context())
				query := cloned.URL.Query()
				query.Set(alias, "sk-review-session-alpha")
				cloned.URL.RawQuery = query.Encode()
				s.Handler().ServeHTTP(w, cloned)
			})
			owner := initializeReviewSession(t, queryHandler, "2025-11-25", "")
			assertReviewCatalog(t, queryHandler, owner, false)
			for _, form := range []string{reviewAlpha, "sk-review-session-alpha", "Bearer old-label@sk-review-session-alpha"} {
				headers := owner.Clone()
				headers.Set("Authorization", form)
				assertReviewCatalog(t, s.Handler(), headers, false)
			}
			for _, header := range []string{reviewBeta, "Bearer"} {
				headers := owner.Clone()
				headers.Set("Authorization", header)
				response := sendSDKWireRequest(t, queryHandler, reviewSessionMessage("tools/list"), headers)
				require.GreaterOrEqual(t, response.Code, 400, "Authorization must win over the query")
				require.Less(t, response.Code, 500)
			}
			assertReviewCatalog(t, queryHandler, owner, false)
		})
	}
}

// TestMCPReviewSessionAcrossReplicas requires no local identity cache or sticky routing.
func TestMCPReviewSessionAcrossReplicas(t *testing.T) {
	first := newReviewSessionServer(t)
	second := newReviewSessionServer(t)
	owner := initializeReviewSession(t, first.Handler(), "2025-11-25", reviewAlpha)
	assertReviewCatalog(t, second.Handler(), owner, false)

	// Streaming GET and DELETE remain usable by the owner on another instance.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		request := httptest.NewRequest(method, "/mcp/", nil).WithContext(ctx)
		request.Header = owner.Clone()
		request.Header.Set("Accept", "text/event-stream")
		response := httptest.NewRecorder()
		second.Handler().ServeHTTP(response, request)
		cancel()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
}

// TestMCPReviewAnonymousSessionDoesNotBecomeAuthenticated preserves public discovery without rebinding.
func TestMCPReviewAnonymousSessionDoesNotBecomeAuthenticated(t *testing.T) {
	s := newReviewSessionServer(t)
	public := initializeReviewSession(t, s.Handler(), "2025-11-25", "")
	assertReviewCatalog(t, s.Handler(), public, true)
	credentialed := public.Clone()
	credentialed.Set("Authorization", reviewAlpha)
	response := sendSDKWireRequest(t, s.Handler(), reviewSessionMessage("tools/list"), credentialed)
	require.GreaterOrEqual(t, response.Code, 400)
	require.Less(t, response.Code, 500)
	assertReviewCatalog(t, s.Handler(), public, true)
	owner := initializeReviewSession(t, s.Handler(), "2025-11-25", reviewAlpha)
	assertReviewCatalog(t, s.Handler(), owner, false)
}
