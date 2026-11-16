package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// redirectingAuthority answers every request with a 307 to elsewhere, which
// accepts any key and counts the requests that arrived with a credential.
// Both listen on 127.0.0.1, so Go's own redirect policy keeps the
// Authorization header: it compares host names only, not ports or schemes.
func redirectingAuthority(t *testing.T) (authority string, forwarded *atomic.Int64) {
	t.Helper()
	forwarded = &atomic.Int64{}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			forwarded.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remain_quota":1}}`))
	}))
	t.Cleanup(elsewhere.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	return redirector.URL, forwarded
}

// TestOneAPIValidatorRefusesRedirectsWithInjectedClient reproduces review
// finding B of PR #95: only the validator's default client refused
// redirects, so an injected client forwarded the bearer to the redirect
// target and accepted that target's answer as one-api's verdict. Whatever
// client is injected, a redirect must be no verdict, the key must stay with
// the configured authority, and the caller's client must stay unmodified.
func TestOneAPIValidatorRefusesRedirectsWithInjectedClient(t *testing.T) {
	authority, forwarded := redirectingAuthority(t)
	injected := &http.Client{Timeout: 5 * time.Second}
	validator, err := NewOneAPITokenValidator(authority, injected)
	require.NoError(t, err)

	err = validator.ValidateAPIKey(context.Background(), "sk-redirect-probe")
	require.ErrorIs(t, err, ErrValidationUnavailable, "a redirect is no verdict")
	require.False(t, errors.Is(err, ErrCredentialRejected))
	_, err = validator.FetchBalance(context.Background(), "sk-redirect-probe")
	require.ErrorIs(t, err, ErrValidationUnavailable)

	require.Zero(t, forwarded.Load(), "the bearer must never reach a redirect target")
	require.Nil(t, injected.CheckRedirect, "the injected client is copied, not modified")
	require.Equal(t, 5*time.Second, injected.Timeout)
}
