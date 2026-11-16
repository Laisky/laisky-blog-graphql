package oneapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheckUserExternalBillingNeverFollowsRedirects keeps the consume call,
// which carries the caller's one-api key like validation does, on the
// configured authority: a redirect answer is not followed, so the key and the
// charge never reach the redirect target. Both servers listen on 127.0.0.1,
// where Go's default policy would forward Authorization across ports.
func TestCheckUserExternalBillingNeverFollowsRedirects(t *testing.T) {
	var forwarded atomic.Int64
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			forwarded.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	original := BillingAPI
	BillingAPI = redirector.URL
	t.Cleanup(func() { BillingAPI = original })

	err := CheckUserExternalBilling(context.Background(), "sk-redirect-probe", PriceWebSearch, "redirect probe")
	require.Error(t, err)
	var billingErr *BillingError
	require.ErrorAs(t, err, &billingErr)
	require.Equal(t, http.StatusTemporaryRedirect, billingErr.Status)
	require.Zero(t, forwarded.Load(), "the key and the charge must never reach a redirect target")
}

// TestCheckUserExternalBillingKeepsTheDefaultClientTransport preserves the
// pre-existing contract that consume calls use http.DefaultClient's
// transport, which other packages' tests and any process-wide transport
// configuration rely on; only the redirect policy is overridden.
func TestCheckUserExternalBillingKeepsTheDefaultClientTransport(t *testing.T) {
	var intercepted atomic.Int64
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		intercepted.Add(1)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://elsewhere.example/"}},
			Body: http.NoBody, Request: r}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previous })

	err := CheckUserExternalBilling(context.Background(), "sk-transport-probe", PriceWebSearch, "transport probe")
	var billingErr *BillingError
	require.ErrorAs(t, err, &billingErr)
	require.Equal(t, http.StatusFound, billingErr.Status)
	require.Equal(t, int64(1), intercepted.Load(), "the redirect is returned, not followed through the default transport")
	require.Nil(t, http.DefaultClient.CheckRedirect, "the process default client is not modified")
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls the adapted function.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestConsumeAtReceiptRequiresOneAPISuccessEnvelope pins what a consume
// answer proves about the key: only one-api's 200 JSON envelope with
// "success": true. The billing result is unchanged: any 200 is an accepted
// charge, so a proxy page still bills exactly as before.
func TestConsumeAtReceiptRequiresOneAPISuccessEnvelope(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType, body string
		accepted          bool
	}{
		"success envelope":    {"application/json; charset=utf-8", `{"success":true,"message":"","data":null}`, true},
		"non-success":         {"application/json", `{"success":false}`, false},
		"html page":           {"text/html", `<html>ok</html>`, false},
		"no content type":     {"", `{"success":true}`, false},
		"malformed json":      {"application/json", `{"success":true`, false},
		"oversized envelope":  {"application/json", `{"success":true,"pad":"` + strings.Repeat("x", 70<<10) + `"}`, false},
		"empty 200 with json": {"application/json", ``, false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				} else {
					w.Header()["Content-Type"] = nil
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			receipt, err := ConsumeAt(context.Background(), server.URL+"/", "sk-receipt-probe", 0, "receipt probe")
			require.NoError(t, err, "any 200 stays an accepted charge")
			require.Equal(t, tc.accepted, receipt.TokenAccepted)
		})
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refused.Close()
	receipt, err := ConsumeAt(context.Background(), refused.URL, "sk-receipt-probe", 0, "receipt probe")
	require.Error(t, err)
	require.False(t, receipt.TokenAccepted)
}
