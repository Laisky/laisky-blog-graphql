package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
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

	r := &QueryResolver{}
	quota, err := r.ValidateOneapiApiKey(context.Background(), apiKey)
	require.NoError(t, err)
	require.NotNil(t, quota)
	require.Equal(t, 1.0, quota.RemainQuota)
	require.Equal(t, 2.0, quota.UsedQuota)
}
