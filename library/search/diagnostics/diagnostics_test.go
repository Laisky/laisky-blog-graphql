package diagnostics

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

func TestSummaryUsesTypedEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, stage, category string
		err                   error
	}{
		{"cancel", "transport", "canceled", context.Canceled},
		{"deadline", "transport", "deadline", context.DeadlineExceeded},
		{"dns", "transport", "dns", &net.DNSError{Err: "private-canary", Name: "private-canary"}},
		{"tls", "transport", "tls", x509.HostnameError{Host: "private-canary"}},
		{"connection", "transport", "connection", syscall.ECONNREFUSED},
		{"timeout", "transport", "timeout", &net.DNSError{IsTimeout: true}},
		{"decode", "decode", "malformed_response", &json.SyntaxError{Offset: 1}},
		{"unknown", "provider", "unknown", errors.New("DNS TLS 429 timeout private-canary")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := Wrap(&url.Error{Op: "Get", URL: "https://private-canary?key=private-canary", Err: tt.err}, tt.stage)
			d := Summary(wrapped)
			want := tt.category
			if tt.name == "timeout" {
				want = "dns"
			} // A DNS timeout is supported DNS evidence, not a generic timeout.
			require.Equal(t, want, d.Category)
			require.Equal(t, tt.stage, d.Stage)
			require.ErrorIs(t, wrapped, tt.err)
			var leakedURL *url.Error
			require.False(t, errors.As(wrapped, &leakedURL))
			require.NotContains(t, wrapped.Error(), "canary")
		})
	}
}

func TestHTTPAndBounds(t *testing.T) {
	for status, category := range map[int]string{401: "auth", 403: "auth", 429: "rate_limit", 500: "provider_http"} {
		d := Summary(HTTP(status))
		require.Equal(t, category, d.Category)
		require.Equal(t, status, d.Status)
	}
	d := Normalize(Diagnostic{Stage: "canary", Category: "canary", Provider: "canary", Status: 999, Attempts: 999})
	require.Equal(t, Diagnostic{Stage: "provider", Category: "unknown", Provider: "unknown", Attempts: 100}, d)
}
