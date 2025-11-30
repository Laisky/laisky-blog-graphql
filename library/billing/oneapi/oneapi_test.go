package oneapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckUserExternalBilling_SendsExpectedPayload(t *testing.T) {
	t.Parallel()

	var (
		receivedAuth        string
		receivedContentType string
		receivedPayload     map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedContentType = r.Header.Get("Content-Type")
		defer r.Body.Close()
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedPayload), "failed to decode payload")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	originalBillingAPI := BillingAPI
	BillingAPI = srv.URL
	defer func() {
		BillingAPI = originalBillingAPI
	}()

	err := CheckUserExternalBilling(context.Background(), "test-api-key", PriceWebSearch, "unit test")
	require.NoError(t, err, "CheckUserExternalBilling returned error")

	require.Equal(t, "Bearer test-api-key", receivedAuth, "unexpected Authorization header")
	require.Equal(t, "application/json", receivedContentType, "unexpected Content-Type header")

	phase, ok := receivedPayload["phase"].(string)
	require.True(t, ok, "phase should be a string")
	require.Equal(t, "single", phase, "unexpected phase in payload")

	quota, ok := receivedPayload["add_used_quota"].(float64)
	require.True(t, ok, "add_used_quota should be a float64")
	require.Equal(t, PriceWebSearch.Int(), int(quota), "unexpected add_used_quota in payload")

	reason, ok := receivedPayload["add_reason"].(string)
	require.True(t, ok, "add_reason should be a string")
	require.Equal(t, "unit test", reason, "unexpected add_reason in payload")
}
