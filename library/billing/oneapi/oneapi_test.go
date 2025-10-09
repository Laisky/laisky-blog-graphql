package oneapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	originalBillingAPI := BillingAPI
	BillingAPI = srv.URL
	defer func() {
		BillingAPI = originalBillingAPI
	}()

	if err := CheckUserExternalBilling(context.Background(), "test-api-key", PriceWebSearch, "unit test"); err != nil {
		t.Fatalf("CheckUserExternalBilling returned error: %v", err)
	}

	if expected := "Bearer test-api-key"; receivedAuth != expected {
		t.Fatalf("unexpected Authorization header: got %q want %q", receivedAuth, expected)
	}

	if expected := "application/json"; receivedContentType != expected {
		t.Fatalf("unexpected Content-Type header: got %q want %q", receivedContentType, expected)
	}

	if phase, ok := receivedPayload["phase"].(string); !ok || phase != "single" {
		t.Fatalf("unexpected phase in payload: %#v", receivedPayload["phase"])
	}

	if quota, ok := receivedPayload["add_used_quota"].(float64); !ok || int(quota) != PriceWebSearch.Int() {
		t.Fatalf("unexpected add_used_quota in payload: %#v", receivedPayload["add_used_quota"])
	}

	if reason, ok := receivedPayload["add_reason"].(string); !ok || reason != "unit test" {
		t.Fatalf("unexpected add_reason in payload: %#v", receivedPayload["add_reason"])
	}
}
