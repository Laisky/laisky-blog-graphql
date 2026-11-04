package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

// graphqlSocket sends a GraphQL request over the test server's real TCP socket
// and returns the decoded response to the caller using a bounded HTTP client.
func graphqlSocket(t *testing.T, server *httptest.Server, bearer, query string, variables map[string]any) graphQLResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/query", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	var result graphQLResponse
	require.NoError(t, json.Unmarshal(raw, &result))
	return result
}

// TestMongoSSOLoginSocketRetained keeps legacy Mongo password login and identity
// checks on a real HTTP connection. It needs MONGO_QUERY_TEST_URI and exercises
// the real generated GraphQL/controller/service/DAO stack, not two processes.
func TestMongoSSOLoginSocketRetained(t *testing.T) {
	e := newSSOE2E(t)
	server := httptest.NewServer(e.engine)
	t.Cleanup(server.Close)
	owner := e.insertLegacyUser(t, "socket-owner@example.test", legacyOwnerPassword, true, nil)
	var before bson.M
	require.NoError(t, e.db.Collection("users").FindOne(t.Context(), bson.M{"_id": owner.ID}).Decode(&before))
	login := func(password string) graphQLResponse {
		return graphqlSocket(t, server, "", ssoLoginMutation,
			map[string]any{"account": owner.Account, "password": password})
	}
	token := stringField(t, login(owner.Password), "UserLogin", "token")
	identity := graphqlSocket(t, server, token, ssoWhoAmIQuery, nil)
	require.Equal(t, owner.UID, stringField(t, identity, "WhoAmI", "id"))
	profile := graphqlSocket(t, server, token, ssoProfileQuery, nil)
	require.Equal(t, owner.UID, stringField(t, profile, "UserProfile", "uid"))
	require.Contains(t, errorMessage(t, login(owner.Password+"x")), "invalid credentials")
	require.NotEmpty(t, graphqlSocket(t, server, "malformed-synthetic-session", ssoWhoAmIQuery, nil).Errors)

	var after bson.M
	require.NoError(t, e.db.Collection("users").FindOne(t.Context(), bson.M{"_id": owner.ID}).Decode(&after))
	require.Equal(t, before["password"], after["password"], "retained login must not replace the legacy password writer")
	require.NotContains(t, after, "status", "retained login must not migrate the legacy account")
}

// TestMongoSSOPasskeySocketRetained sends real signed passkey assertions and
// resulting identity requests over TCP for both retained legacy and UUID handles.
func TestMongoSSOPasskeySocketRetained(t *testing.T) {
	e := newSSOE2E(t)
	server := httptest.NewServer(e.engine)
	t.Cleanup(server.Close)
	owner := e.insertLegacyUser(t, "socket-passkey@example.test", legacyOwnerPassword, true, nil)
	for _, tc := range []struct {
		name   string
		handle []byte
	}{
		{name: "legacy_object_id", handle: []byte(owner.ID.Hex())},
		{name: "current_uuid", handle: []byte(owner.UID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := newVirtualPasskey(t)
			e.addPasskey(t, owner.ID, key.storedPasskey(t))
			start := graphqlSocket(t, server, "", ssoPasskeyStartMutation, nil)
			optionsJSON := stringField(t, start, "UserStartPasskeyLogin", "options_json")
			session := stringField(t, start, "UserStartPasskeyLogin", "session")
			var options struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
				} `json:"publicKey"`
			}
			require.NoError(t, json.Unmarshal([]byte(optionsJSON), &options))
			require.NotEmpty(t, options.PublicKey.Challenge)
			assertion := key.assertion(t, options.PublicKey.Challenge, ssoE2EOrigin, tc.handle, key.counter+1)
			finish := graphqlSocket(t, server, "", ssoPasskeyFinishMutation,
				map[string]any{"session": session, "credential": assertion})
			token := stringField(t, finish, "UserFinishPasskeyLogin", "token")
			identity := graphqlSocket(t, server, token, ssoWhoAmIQuery, nil)
			require.Equal(t, owner.UID, stringField(t, identity, "WhoAmI", "id"))
		})
	}
}
