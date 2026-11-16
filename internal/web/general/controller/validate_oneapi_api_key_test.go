package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	gmw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestValidateOneapiAPIKey_Success verifies the resolver can validate a OneAPI key and parse quota response.
func TestValidateOneapiAPIKey_Success(t *testing.T) {
	var called int32
	apiKey := "unit-test-key"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"message": "",
			"data": map[string]any{
				"remain_quota": 123.0,
				"used_quota":   456.0,
			},
		})
	}))
	t.Cleanup(ts.Close)

	old := gconfig.Shared.GetString("settings.oneapi.balance_url")
	gconfig.Shared.Set("settings.oneapi.balance_url", ts.URL)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.oneapi.balance_url", old)
	})
	installAuthenticatorFromConfig(t)

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiAPIKey(context.Background(), apiKey)
	require.NoError(t, err)
	require.NotNil(t, quota)
	require.Equal(t, 123.0, quota.RemainQuota)
	require.Equal(t, 456.0, quota.UsedQuota)
	require.Equal(t, int32(1), atomic.LoadInt32(&called))
}

// TestValidateOneapiAPIKey_Non200 verifies non-200 status returns error.
func TestValidateOneapiAPIKey_Non200(t *testing.T) {
	apiKey := "unit-test-key"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	old := gconfig.Shared.GetString("settings.oneapi.balance_url")
	gconfig.Shared.Set("settings.oneapi.balance_url", ts.URL)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.oneapi.balance_url", old)
	})
	installAuthenticatorFromConfig(t)

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiAPIKey(context.Background(), apiKey)
	require.Error(t, err)
	require.Nil(t, quota)
}

// TestValidateOneapiAPIKey_Unsuccessful verifies success=false returns error.
func TestValidateOneapiAPIKey_Unsuccessful(t *testing.T) {
	apiKey := "unit-test-key"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "invalid token",
			"data": map[string]any{
				"remain_quota": 0.0,
				"used_quota":   0.0,
			},
		})
	}))
	t.Cleanup(ts.Close)

	old := gconfig.Shared.GetString("settings.oneapi.balance_url")
	gconfig.Shared.Set("settings.oneapi.balance_url", ts.URL)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.oneapi.balance_url", old)
	})
	installAuthenticatorFromConfig(t)

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiAPIKey(context.Background(), apiKey)
	require.Error(t, err)
	require.Nil(t, quota)
}

// TestValidateOneapiApiKey_BackCompat verifies the deprecated method still works.
func TestValidateOneapiApiKey_BackCompat(t *testing.T) {
	apiKey := "unit-test-key"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"message": "",
			"data": map[string]any{
				"remain_quota": 1.0,
				"used_quota":   2.0,
			},
		})
	}))
	t.Cleanup(ts.Close)

	old := gconfig.Shared.GetString("settings.oneapi.balance_url")
	gconfig.Shared.Set("settings.oneapi.balance_url", ts.URL)
	t.Cleanup(func() {
		gconfig.Shared.Set("settings.oneapi.balance_url", old)
	})
	installAuthenticatorFromConfig(t)

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiApiKey(context.Background(), apiKey)
	require.NoError(t, err)
	require.NotNil(t, quota)
	require.Equal(t, 1.0, quota.RemainQuota)
	require.Equal(t, 2.0, quota.UsedQuota)
}

// installAuthenticatorFromConfig installs the production authenticator built
// from the current config, so the resolver reaches the fixture through the
// shared one-api gate exactly as in production (legacy balance_url override).
func installAuthenticatorFromConfig(t *testing.T) {
	t.Helper()
	authenticator, err := mcpauth.NewAuthenticatorFromSettings(mcpauth.LoadValidationSettingsFromConfig(), nil)
	require.NoError(t, err)
	authtest.Swap(t, authenticator)
}

// TestValidateOneapiAPIKeyRefusesOversizedKeyBeforeHashing: the GraphQL
// argument is bounded only by the body budget, so an oversized key is
// refused like Balance refuses it, before the resolver hashes it for its log
// fingerprint and without any authority call.
func TestValidateOneapiAPIKeyRefusesOversizedKeyBeforeHashing(t *testing.T) {
	var calls atomic.Int64
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(authority.Close)
	validator, err := mcpauth.NewOneAPITokenValidator(authority.URL, nil)
	require.NoError(t, err)
	authenticator, err := mcpauth.NewAuthenticator(validator, mcpauth.AuthenticatorOptions{Name: "oneapi"})
	require.NoError(t, err)
	authtest.Swap(t, authenticator)
	core, observed := observer.New(zap.DebugLevel)
	logger, err := glog.NewWithName("validate", glog.LevelDebug, zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	ctx := gmw.SetLogger(context.Background(), logger)

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiAPIKey(ctx, " sk-"+strings.Repeat("k", mcpauth.MaxAPIKeyLength)+" ")
	require.ErrorIs(t, err, mcpauth.ErrInvalidAuthorization)
	require.Nil(t, quota)
	require.Zero(t, calls.Load())
	for _, entry := range observed.All() {
		_, fingerprinted := entry.ContextMap()["key_fingerprint"]
		require.False(t, fingerprinted, "an oversized key is never hashed: %q", entry.Message)
	}

	quota, err = r.ValidateOneapiAPIKey(ctx, strings.Repeat("k", mcpauth.MaxAPIKeyLength))
	require.ErrorIs(t, err, mcpauth.ErrCredentialRejected, "a key of exactly the maximum length still reaches the authority")
	require.Nil(t, quota)
	require.Equal(t, int64(1), calls.Load())
}
