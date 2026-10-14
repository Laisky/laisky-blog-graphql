package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

func TestLogErrorPreservesOrdinaryCauseAndIdentity(t *testing.T) {
	cause := errors.New("navigation failed: unexpected EOF; api_key=private-key-canary; response body: private-page-canary")
	original := WithProvider(Wrap(cause, providerStage), "google")
	before := Summary(original)
	text := LogError(original).Error()
	require.Contains(t, text, "unexpected EOF")
	require.NotContains(t, text, "canary")
	require.ErrorIs(t, original, cause)
	require.Equal(t, before, Summary(original))
	require.NotErrorIs(t, LogError(original), cause, "presentation cannot unwrap to a raw provider error")
	require.Nil(t, LogError(nil))
}

func TestLogErrorDoesNotFormatExternalContent(t *testing.T) {
	for _, tc := range []struct {
		name, stage, useful string
		cause               error
	}{
		{"dns", transportStage, "resolver.fixture.test", &net.DNSError{Err: "resolver error api_key=private-dns-canary", Name: "resolver.fixture.test"}},
		{"tls", transportStage, "tls.fixture.test", x509.HostnameError{Host: "tls.fixture.test"}},
		{"certificate", transportStage, "certificate expired or not yet valid", &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired, Detail: "private-certificate-detail-canary"}}},
		{"header", transportStage, "header parser rejected", &http.ProtocolError{ErrorString: "header parser rejected headers: private-header-response-canary"}},
		{"syntax", decodeStage, "malformed JSON response", &json.SyntaxError{Offset: 1}},
		{"shape", decodeStage, "JSON response type mismatch", &json.UnmarshalTypeError{Value: "private-body-canary", Field: "private-field-canary"}},
		{"labeled response", responseStage, "response read failed", errors.New("response read failed response body: private-response-canary")},
		{"labeled transport", transportStage, "transport read failed", errors.New("transport read failed headers: private-header-canary")},
		{"EOF", responseStage, "unexpected EOF", io.ErrUnexpectedEOF},
		{"connection", transportStage, "connection refused", syscall.ECONNREFUSED},
		{"canceled", transportStage, "context canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := Wrap(tc.cause, tc.stage)
			projection := LogError(original)
			require.Contains(t, projection.Error(), tc.useful)
			require.NotContains(t, projection.Error(), "canary")
			require.ErrorIs(t, original, tc.cause)
		})
	}
}

func TestLogErrorJoinedCausesRemainHonest(t *testing.T) {
	first := WithProvider(Wrap(io.ErrUnexpectedEOF, transportStage), "google")
	second := WithProvider(HTTP(429), "firecrawl")
	original := Exhausted([]error{first, second})
	projection := LogError(original)
	require.Contains(t, projection.Error(), "unexpected EOF")
	require.Contains(t, projection.Error(), "rate_limit")
	require.Equal(t, unknownValue, Summary(original).Category)
	require.Equal(t, managerStage, Summary(original).Stage)
	require.Zero(t, Summary(original).Status)
	require.ErrorIs(t, original, io.ErrUnexpectedEOF)
	require.ErrorIs(t, original, second)
}

func TestLogErrorFixedConfigurationContext(t *testing.T) {
	original := errors.Wrap(New("configuration", "internal"), "google api key is not configured")
	require.Contains(t, LogError(original).Error(), "google api key is not configured")
}

type cyclicLogCause struct{}

func (cyclicLogCause) Error() string   { return "private-cyclic-canary" }
func (c cyclicLogCause) Unwrap() error { return c }

type panicLogCause struct{}

func (panicLogCause) Error() string { panic("private-panic-canary") }

func TestLogErrorBoundedProjection(t *testing.T) {
	original := &FailureError{diagnostic: Normalize(Diagnostic{Stage: providerStage, Category: unknownValue}), cause: cyclicLogCause{}}
	require.Contains(t, LogError(original).Error(), "additional diagnostic causes omitted")
	require.NotContains(t, LogError(original).Error(), "canary")
	original.cause = panicLogCause{}
	require.Contains(t, LogError(original).Error(), unavailableCauseText)
	require.NotContains(t, LogError(original).Error(), "canary")
	original.cause = errors.New(strings.Repeat("x", diagnosticCauseLimit+1))
	require.Contains(t, LogError(original).Error(), oversizedCauseText)
}

// TestLogErrorRoutineOperationalContext prevents broad suppression of useful harmless causes.
func TestLogErrorRoutineOperationalContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		useful []string
	}{
		{"ordinary transport", errors.New("proxy dial failed: transport currently unavailable"), []string{"proxy dial failed", "transport currently unavailable"}},
		{"DNS routine host", &net.DNSError{Err: "no such host", Name: "resolver.fixture.test", IsNotFound: true}, []string{"resolver.fixture.test", "no such host"}},
		{"ordinary protocol reason", &http.ProtocolError{ErrorString: "unsupported protocol feature h2c"}, []string{"unsupported protocol feature h2c"}},
		{"transport operation", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, []string{"dial tcp", "connection refused"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := Wrap(tc.cause, transportStage)
			text := LogError(original).Error()
			for _, useful := range tc.useful {
				require.Contains(t, text, useful)
			}
			require.ErrorIs(t, original, tc.cause)
		})
	}
}

// TestLogErrorWrappedTLSRoutineReason retains actionable reason without certificate contents.
func TestLogErrorWrappedTLSRoutineReason(t *testing.T) {
	invalid := x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired, Detail: "private-certificate-detail-canary"}
	cause := &tls.CertificateVerificationError{Err: invalid}
	original := Wrap(cause, transportStage)
	text := LogError(original).Error()
	require.Contains(t, text, "certificate expired or not yet valid")
	require.NotContains(t, text, "canary")
	require.ErrorIs(t, original, cause)
}
