package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestMCPReviewModernHTTPMethodsStayUnsupported prevents the ownership guard
// from turning removed modern endpoints into usable legacy streams or sessions.
func TestMCPReviewModernHTTPMethodsStayUnsupported(t *testing.T) {
	s := newReviewSessionServer(t)
	owner := initializeReviewSession(t, s.Handler(), mcpgo.ProtocolVersion20251125, reviewAlpha)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, authorization := range []string{"", reviewAlpha, reviewBeta} {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest(method, "/mcp/", nil).WithContext(ctx)
			request.Header = owner.Clone()
			request.Header.Set(mcpgo.HeaderProtocolVersion, mcpgo.ProtocolVersion20260728)
			request.Header.Set("Accept", "text/event-stream")
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, request)
			require.Equal(t, http.StatusMethodNotAllowed, response.Code, response.Body.String())
		}
	}
	assertReviewCatalog(t, s.Handler(), owner, false)
}

// TestMCPReviewUnversionedHTTPRequestsCannotBorrowSession protects the SDK's
// backwards-compatible default protocol when a legacy client omits the header.
func TestMCPReviewUnversionedHTTPRequestsCannotBorrowSession(t *testing.T) {
	s := newReviewSessionServer(t)
	owner := initializeReviewSession(t, s.Handler(), mcpgo.ProtocolVersion20250326, reviewAlpha)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		request := httptest.NewRequest(method, "/mcp/", nil).WithContext(ctx)
		request.Header = owner.Clone()
		request.Header.Del(mcpgo.HeaderProtocolVersion)
		request.Header.Set("Authorization", reviewBeta)
		request.Header.Set("Accept", "text/event-stream")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	assertReviewCatalog(t, s.Handler(), owner, false)
}
