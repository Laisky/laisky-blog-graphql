package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// githubRedirectTransport answers every api.github.com request with a
// redirect to target and forwards every other request (the token endpoint,
// the target) to the real servers, so a client that follows the redirect
// really delivers the request to the second server.
type githubRedirectTransport struct {
	status int
	target *url.URL
}

// RoundTrip redirects GitHub API requests and forwards everything else.
func (rt githubRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	location := *req.URL
	location.Scheme, location.Host = rt.target.Scheme, rt.target.Host
	return &http.Response{StatusCode: rt.status, Header: http.Header{"Location": []string{location.String()}}, Body: http.NoBody, Request: req}, nil
}

// TestGitHubOAuthRefusesRedirects: the code exchange carries the OAuth client
// secret and every API call the user's access token (the oauth2 transport
// sets it again on each hop, cross-domain included), so neither may follow a
// redirect; a redirect answer fails like any other non-2xx status.
func TestGitHubOAuthRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits, leaked atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits.Add(1)
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(r.Header.Get("Authorization"), "gho_user_token") || strings.Contains(string(body), "client-secret") {
				leaked.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"login":"octo","email":"octo@example.com","access_token":"gho_leaked","token_type":"bearer"}`))
		}))
		targetURL, err := url.Parse(target.URL)
		require.NoError(t, err)

		token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gho_user_token","token_type":"bearer"}`))
		}))
		tokenRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/login/oauth/access_token", status)
		}))

		base := &http.Client{Transport: githubRedirectTransport{status: status, target: targetURL}}
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
		for name, tokenURL := range map[string]string{"api call": token.URL, "code exchange": tokenRedirect.URL} {
			config := &oauth2.Config{
				ClientID: "client-id", ClientSecret: "client-secret",
				Endpoint: oauth2.Endpoint{AuthURL: token.URL + "/authorize", TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
			}
			user, email, err := fetchGitHubOAuthUser(ctx, config, "code")
			require.Error(t, err, "%s, status %d", name, status)
			require.Nil(t, user)
			require.Empty(t, email)
		}
		require.Nil(t, base.CheckRedirect, "the context's client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: neither the client secret nor the user's token may reach a redirect target", status)
		require.Zero(t, leaked.Load())
		tokenRedirect.Close()
		token.Close()
		target.Close()
	}
}
