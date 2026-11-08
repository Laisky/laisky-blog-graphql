package ssoredirect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// sharedVectors mirrors web/src/lib/sso-redirect-policy.v1.json, which the frontend tests consume unchanged.
type sharedVectors struct {
	Version      int      `json:"version"`
	IssuerOrigin string   `json:"issuerOrigin"`
	Clients      []Client `json:"clients"`
	Cases        []struct {
		Target string `json:"target"`
		Pad    int    `json:"pad"`
		Allow  string `json:"allow"`
		Deny   string `json:"deny"`
	} `json:"cases"`
	Registrations []struct {
		URI   string `json:"uri"`
		Valid bool   `json:"valid"`
	} `json:"registrations"`
}

// loadSharedVectors reads the cross-language fixture from the frontend source tree.
func loadSharedVectors(t *testing.T) sharedVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "lib", "sso-redirect-policy.v1.json"))
	require.NoError(t, err)
	var vectors sharedVectors
	require.NoError(t, json.Unmarshal(raw, &vectors))
	require.Equal(t, 1, vectors.Version)
	require.NotEmpty(t, vectors.Cases)
	return vectors
}

// vectorPolicy parses the fixture registry through the production configuration parser.
func vectorPolicy(t *testing.T, vectors sharedVectors, mode string) Policy {
	t.Helper()
	clients := make(map[string]any, len(vectors.Clients))
	for _, client := range vectors.Clients {
		uris := make([]any, 0, len(client.RedirectURIs))
		for _, uri := range client.RedirectURIs {
			uris = append(uris, uri)
		}
		clients[client.ID] = map[string]any{"redirect_uris": uris, "legacy_token_redirect": client.LegacyTokenRedirect}
	}
	policy, err := Parse(mode, clients)
	require.NoError(t, err)
	require.Equal(t, vectors.Clients, policy.Clients, "the parser must keep the registry canonical")
	return policy
}

// TestSharedRedirectVectors pins the backend classifier to the vectors the frontend also enforces.
func TestSharedRedirectVectors(t *testing.T) {
	vectors := loadSharedVectors(t)
	for _, mode := range []string{ModeAllow, ModeDeny} {
		policy := vectorPolicy(t, vectors, mode)
		for _, item := range vectors.Cases {
			raw := item.Target + strings.Repeat("a", item.Pad)
			want := item.Allow
			if mode == ModeDeny {
				want = item.Deny
			}
			require.Equal(t, want, Classify(raw, vectors.IssuerOrigin, policy).String(), "mode=%s target=%q", mode, raw)
		}
	}
}

// TestSharedRegistrationVectors pins redirect URI registration rules shared with the frontend.
func TestSharedRegistrationVectors(t *testing.T) {
	for _, item := range loadSharedVectors(t).Registrations {
		err := ValidateRedirectURI(item.URI)
		if item.Valid {
			require.NoError(t, err, item.URI)
		} else {
			require.Error(t, err, item.URI)
		}
	}
}

// TestClassifyCodeDecisionCarriesBinding verifies a code decision names the exact registered callback.
func TestClassifyCodeDecisionCarriesBinding(t *testing.T) {
	vectors := loadSharedVectors(t)
	policy := vectorPolicy(t, vectors, ModeDeny)
	state, challenge := strings.Repeat("A", 43), strings.Repeat("C", 42)+"A"
	raw := "https://accounting.laisky.com/api/auth/sso/callback?sso_flow=code&sso_client_id=accounting&sso_state=" + state +
		"&sso_challenge=" + challenge + "&sso_challenge_method=S256"
	decision := Classify(raw, vectors.IssuerOrigin, policy)
	require.Equal(t, KindClientCode, decision.Kind)
	require.Equal(t, "accounting", decision.ClientID)
	require.Equal(t, "https://accounting.laisky.com/api/auth/sso/callback", decision.RedirectURI)
	require.True(t, decision.Allowed())
	require.False(t, Classify("http://cv.laisky.com/", vectors.IssuerOrigin, policy).Allowed())
	require.Equal(t, "self", Classify("/profile", "https://sso.laisky.com/", policy).String())
	require.Equal(t, "reject:unsupported_reference", Classify("/profile", "", policy).String())
	require.Equal(t, "reject:unsupported_reference", Classify("/profile", "https://sso.laisky.com/base", policy).String())
}

// TestParsePolicyDefaultsAndFailClosed verifies compatible defaults and fail-closed loading.
func TestParsePolicyDefaultsAndFailClosed(t *testing.T) {
	policy, err := Parse(nil, nil)
	require.NoError(t, err)
	require.Equal(t, ModeAllow, policy.LegacyTokenRedirect, "the legacy handoff stays enabled by default")
	require.Empty(t, policy.Clients)

	policy, err = Parse(" DENY ", map[string]any{"accounting": map[string]any{
		"redirect_uris": []string{"https://accounting.laisky.com/cb", "https://accounting.laisky.com/cb"}}})
	require.NoError(t, err)
	require.Equal(t, ModeDeny, policy.LegacyTokenRedirect)
	require.Equal(t, []Client{{ID: "accounting", RedirectURIs: []string{"https://accounting.laisky.com/cb"}, LegacyTokenRedirect: ModeDeny}},
		policy.Clients)
	require.True(t, policy.ValidCodeBinding("accounting", "https://accounting.laisky.com/cb"))
	require.True(t, policy.ValidCodeBinding(BlogClientID, BlogRedirectURI))
	require.False(t, policy.ValidCodeBinding(BlogClientID, "https://accounting.laisky.com/cb"))
	origin, ok := policy.RedeemOrigin("accounting", "https://accounting.laisky.com/cb")
	require.True(t, ok)
	require.Equal(t, "https://accounting.laisky.com", origin)
	require.True(t, policy.RedeemOriginAllowed("https://accounting.laisky.com"))
	require.True(t, policy.RedeemOriginAllowed(BlogRedirectURI))
	require.False(t, policy.RedeemOriginAllowed("https://other.laisky.com"))
	require.Equal(t, "sso:blog:code:", CodeKeyPrefix(BlogClientID))
	require.Equal(t, "sso:client:accounting:code:", CodeKeyPrefix("accounting"))

	for _, item := range []struct {
		mode    any
		clients any
	}{
		{mode: "maybe"},
		{mode: 1},
		{clients: []any{"accounting"}},
		{clients: map[string]any{"blog": map[string]any{"redirect_uris": []any{"https://x.laisky.com/cb"}}}},
		{clients: map[string]any{"accounting": map[string]any{"redirect_uris": []any{1}}}},
		{clients: map[string]any{"accounting": map[string]any{"redirect_uris": []any{"https://x.laisky.com/cb"}, "legacy_token_redirect": "x"}}},
	} {
		policy, err := Parse(item.mode, item.clients)
		require.Error(t, err)
		require.Equal(t, FailClosed(), policy)
	}

	oldMode := gconfig.Shared.Get(ConfigKeyLegacyTokenRedirect)
	t.Cleanup(func() { gconfig.Shared.Set(ConfigKeyLegacyTokenRedirect, oldMode) })
	gconfig.Shared.Set(ConfigKeyLegacyTokenRedirect, "unknown")
	policy, err = Load()
	require.Error(t, err)
	require.Equal(t, ModeDeny, policy.LegacyTokenRedirect, "an unreadable policy must fail closed")
}
