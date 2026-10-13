package google

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v7"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

const diagnosticCanary = "private-query-key-body-url-canary"

// TestSearchDiagnosticsPrivacy exercises actual JSON log encoding and error text offline.
func TestSearchDiagnosticsPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		transportErr error
		readErr      error
		success      bool
	}{
		{name: "success", status: 200, body: `{"items":[]}`, success: true},
		{name: "auth", status: 401, body: diagnosticCanary},
		{name: "forbidden", status: 403, body: diagnosticCanary},
		{name: "rate_limit", status: 429, body: diagnosticCanary},
		{name: "provider_http", status: 503, body: diagnosticCanary},
		{name: "malformed", status: 200, body: `{"items":"` + diagnosticCanary + `"}`},
		{name: "canceled", transportErr: context.Canceled},
		{name: "deadline", transportErr: context.DeadlineExceeded},
		{name: "dns", transportErr: &net.DNSError{Err: diagnosticCanary, Name: diagnosticCanary, IsNotFound: true}},
		{name: "tls", transportErr: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
		{name: "connection", transportErr: syscall.ECONNREFUSED},
		{name: "timeout", transportErr: diagnosticTimeout{}},
		{name: "unknown_transport", transportErr: errors.New(diagnosticCanary)},
		{name: "read", status: 200, readErr: errors.New(diagnosticCanary)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var encoded bytes.Buffer
			core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
				zapcore.AddSync(&encoded), zap.DebugLevel)
			logger, err := glog.NewWithName("test", glog.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			calls := 0
			client := &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, diagnosticCanary, req.URL.Query().Get("q"))
				require.Equal(t, diagnosticCanary, req.URL.Query().Get("key"))
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				body := io.ReadCloser(io.NopCloser(strings.NewReader(tc.body)))
				if tc.readErr != nil {
					body = diagnosticReader{err: tc.readErr}
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body, Request: req}, nil
			})}
			engine := NewSearchEngine(diagnosticCanary, "test-cx")
			engine.client = client
			engine.logger = logger
			_, err = engine.Search(gmw.SetLogger(context.Background(), logger), diagnosticCanary)
			require.Equal(t, 1, calls, "privacy checks must actually invoke the provider")
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				logger.Error("provider test failure", zap.Error(err))
				require.NotContains(t, err.Error(), diagnosticCanary)
				stage, category := "transport", tc.name
				switch tc.name {
				case "auth", "forbidden":
					stage, category = "response", "auth"
				case "rate_limit", "provider_http":
					stage = "response"
				case "malformed":
					stage, category = "decode", "malformed_response"
				case "deadline":
					category = "timeout"
				case "unknown_transport":
					category = "unknown"
				case "read":
					stage, category = "response", "unknown"
				}
				summary := diagnostics.Summary(err)
				require.Equal(t, stage, summary.Stage)
				require.Equal(t, category, summary.Category)
				if tc.status != 0 && tc.status != 200 {
					require.Equal(t, tc.status, summary.Status)
				}
				if tc.transportErr != nil {
					require.ErrorIs(t, err, tc.transportErr)
					var requestErr *url.Error
					require.False(t, errors.As(err, &requestErr), "credential-bearing request URL must be removed")
				}
				if tc.readErr != nil {
					require.ErrorIs(t, err, tc.readErr)
				}
			}
			require.NotEmpty(t, encoded.String(), "safe telemetry must remain enabled")
			require.NotContains(t, encoded.String(), diagnosticCanary)
			require.NotContains(t, encoded.String(), "Authorization")
			require.NotContains(t, encoded.String(), "api_key")
		})
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type diagnosticReader struct{ err error }

func (r diagnosticReader) Read([]byte) (int, error) { return 0, r.err }
func (r diagnosticReader) Close() error             { return nil }

type diagnosticTimeout struct{}

func (diagnosticTimeout) Error() string   { return diagnosticCanary }
func (diagnosticTimeout) Timeout() bool   { return true }
func (diagnosticTimeout) Temporary() bool { return true }

func TestSearchHTTPStatusDoesNotDependOnBody(t *testing.T) {
	reads := 0
	engine := NewSearchEngine(diagnosticCanary, "test-cx")
	engine.client = &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: diagnosticCountingReader{reads: &reads}, Request: req}, nil
	})}
	_, err := engine.Search(context.Background(), diagnosticCanary)
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, diagnostics.Summary(err).Status)
	require.Equal(t, "rate_limit", diagnostics.Summary(err).Category)
	require.Zero(t, reads, "failure response bodies are neither needed nor consumed")
}

type diagnosticCountingReader struct{ reads *int }

func (r diagnosticCountingReader) Read([]byte) (int, error) {
	*r.reads++
	return 0, errors.New(diagnosticCanary)
}
func (r diagnosticCountingReader) Close() error { return nil }

func TestSearchMissingConfigurationDiagnostics(t *testing.T) {
	engine := NewSearchEngine("", "test-cx")
	_, err := engine.Search(context.Background(), diagnosticCanary)
	require.Error(t, err)
	require.Equal(t, "configuration", diagnostics.Summary(err).Stage)
	require.Equal(t, "internal", diagnostics.Summary(err).Category)
	require.NotContains(t, err.Error(), diagnosticCanary)
}

// TestSearchClientTimeoutIsNotCallerDeadline exercises http.Client.Timeout itself.
func TestSearchClientTimeoutIsNotCallerDeadline(t *testing.T) {
	ctx := context.Background()
	engine := NewSearchEngine(diagnosticCanary, "test-cx")
	engine.client = &http.Client{
		Timeout: 10 * time.Millisecond,
		Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	_, err := engine.Search(ctx, diagnosticCanary)
	require.Error(t, err)
	require.NoError(t, ctx.Err(), "caller context remains live")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "transport", diagnostics.Summary(err).Stage)
	require.Equal(t, "timeout", diagnostics.Summary(err).Category)
	require.NotContains(t, err.Error(), diagnosticCanary)
}

// TestSearchExpiredCallerDeadline retains the caller's lifecycle classification.
func TestSearchExpiredCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	engine := NewSearchEngine(diagnosticCanary, "test-cx")
	engine.client = &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	_, err := engine.Search(ctx, diagnosticCanary)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "transport", diagnostics.Summary(err).Stage)
	require.Equal(t, "deadline", diagnostics.Summary(err).Category)
	require.NotContains(t, err.Error(), diagnosticCanary)
}

// TestSearchStalledResponseBodyClassifiesDeadline distinguishes body reads from sending requests.
func TestSearchStalledResponseBodyClassifiesDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, category               string
		clientTimeout, callerTimeout time.Duration
	}{
		{name: "client_timeout", category: "timeout", clientTimeout: 200 * time.Millisecond, callerTimeout: 5 * time.Second},
		{name: "caller_deadline", category: "deadline", clientTimeout: 5 * time.Second, callerTimeout: 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var encoded bytes.Buffer
			core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
				zapcore.AddSync(&encoded), zap.DebugLevel)
			logger, err := glog.NewWithName("test", glog.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			headersSent := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, diagnosticCanary)
				w.(http.Flusher).Flush()
				close(headersSent)
				<-req.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(gmw.SetLogger(context.Background(), logger), tc.callerTimeout)
			defer cancel()
			client := server.Client()
			client.Timeout = tc.clientTimeout
			engine := NewSearchEngine(diagnosticCanary, "test-cx")
			localTransport := client.Transport
			client.Transport = diagnosticTransport(func(req *http.Request) (*http.Response, error) {
				localRequest := req.Clone(req.Context())
				endpoint, parseErr := url.Parse(server.URL)
				if parseErr != nil {
					return nil, parseErr
				}
				localRequest.URL.Scheme, localRequest.URL.Host = endpoint.Scheme, endpoint.Host
				return localTransport.RoundTrip(localRequest)
			})
			engine.client = client
			_, err = engine.Search(ctx, diagnosticCanary)
			require.Error(t, err)
			select {
			case <-headersSent:
			default:
				t.Fatal("server must flush headers before the body stalls")
			}
			if tc.category == "timeout" {
				require.NoError(t, ctx.Err(), "caller is active when the client times out")
			} else {
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded, "deadline cause remains discoverable")
			var requestErr *url.Error
			require.False(t, errors.As(err, &requestErr), "sensitive request URL must not be retained")
			summary := diagnostics.Summary(err)
			require.Equal(t, "response", summary.Stage)
			require.Equal(t, tc.category, summary.Category)
			logger.Error("provider body test failure", zap.Error(err))
			require.NotEmpty(t, encoded.String(), "diagnostic serialization is exercised")
			require.NotContains(t, encoded.String(), diagnosticCanary)
			require.NotContains(t, err.Error(), diagnosticCanary)
		})
	}
}
