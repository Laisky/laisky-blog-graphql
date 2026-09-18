package oneapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestClassifyBillingOutcome is the G06 contract. A caller must be able to tell
// a definitive rejection from an undetermined remote state, because the two
// require opposite handling: a denial means no quota was added and the request
// simply fails, while an unknown outcome means the consume may already have
// been applied and must never be reported as uncharged or auto-replayed.
func TestClassifyBillingOutcome(t *testing.T) {
	t.Parallel()

	t.Run("no error is an accepted consume", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, BillingAccepted, ClassifyBillingOutcome(nil))
	})

	t.Run("an unclassified error is never silently accepted", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, BillingUnknown, ClassifyBillingOutcome(errors.New("some transport problem")),
			"an error without a classification must default to unknown, not denied")
	})

	t.Run("a classified error survives wrapping", func(t *testing.T) {
		t.Parallel()
		wrapped := errors.Wrap(errors.Wrap(&BillingError{Outcome: BillingDenied, Status: 402,
			Reason: "insufficient quota"}, "check user external billing"), "web_search")
		require.Equal(t, BillingDenied, ClassifyBillingOutcome(wrapped))
	})

	t.Run("each outcome round-trips", func(t *testing.T) {
		t.Parallel()
		for _, outcome := range []BillingOutcome{BillingDenied, BillingUnknown, BillingNotAttempted} {
			require.Equal(t, outcome, ClassifyBillingOutcome(&BillingError{Outcome: outcome}))
		}
	})
}

// TestBillingOutcomeChargeSemantics pins what each outcome means for the
// recorded cost, so one interface cannot present an unknown consume as free
// while another presents it as a receipt.
func TestBillingOutcomeChargeSemantics(t *testing.T) {
	t.Parallel()

	require.True(t, BillingAccepted.Charged(), "an accepted consume posted the quota")
	require.False(t, BillingDenied.Charged(), "a denial adds no quota")
	require.False(t, BillingNotAttempted.Charged(), "nothing was sent")
	require.True(t, BillingUnknown.Charged(),
		"an undetermined consume must be recorded as possibly charged, never as free")

	require.False(t, BillingAccepted.Indeterminate())
	require.False(t, BillingDenied.Indeterminate())
	require.True(t, BillingUnknown.Indeterminate())
	require.False(t, BillingNotAttempted.Indeterminate())

	// Only a definitive denial may be retried by a later explicit action; an
	// unknown outcome must not be replayed automatically.
	require.True(t, BillingDenied.SafeToRetry())
	require.True(t, BillingNotAttempted.SafeToRetry())
	require.False(t, BillingUnknown.SafeToRetry())
	require.False(t, BillingAccepted.SafeToRetry())
}

// TestCheckUserExternalBillingClassifiesRemoteResponses drives the real HTTP
// client against a controlled billing endpoint, so the classification reflects
// what the shipped code actually does with each remote answer.
func TestCheckUserExternalBillingClassifiesRemoteResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		expected BillingOutcome
	}{
		{name: "ok is accepted", status: http.StatusOK, body: `{"success":true}`, expected: BillingAccepted},
		{name: "payment required is denied", status: http.StatusPaymentRequired, body: `insufficient quota`, expected: BillingDenied},
		{name: "unauthorized is denied", status: http.StatusUnauthorized, body: `bad key`, expected: BillingDenied},
		{name: "forbidden is denied", status: http.StatusForbidden, body: `disabled`, expected: BillingDenied},
		{name: "too many requests is denied", status: http.StatusTooManyRequests, body: `slow down`, expected: BillingDenied},
		{name: "server error is unknown", status: http.StatusInternalServerError, body: `oops`, expected: BillingUnknown},
		{name: "bad gateway is unknown", status: http.StatusBadGateway, body: `upstream`, expected: BillingUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/token/consume", r.URL.Path)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			restore := BillingAPI
			BillingAPI = server.URL
			defer func() { BillingAPI = restore }()

			err := CheckUserExternalBilling(context.Background(), "sk-test", PriceWebSearch, "web_search")
			require.Equal(t, tc.expected, ClassifyBillingOutcome(err))
			if tc.expected == BillingAccepted {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			// The remote body may name the account; it must not be echoed verbatim.
			require.NotContains(t, err.Error(), "sk-test")
		})
	}
}

// TestCheckUserExternalBillingTimeoutIsUnknown covers the case the matrix calls
// out explicitly: a timeout leaves the remote state undetermined, so it must
// not be classified as a denial.
func TestCheckUserExternalBillingTimeoutIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	restore := BillingAPI
	BillingAPI = server.URL
	defer func() { BillingAPI = restore }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := CheckUserExternalBilling(ctx, "sk-test", PriceWebSearch, "web_search")
	require.Error(t, err)
	require.Equal(t, BillingUnknown, ClassifyBillingOutcome(err))
}
