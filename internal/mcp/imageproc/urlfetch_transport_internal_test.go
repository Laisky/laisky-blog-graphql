package imageproc

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestURLFetchSecurityRedirectAndPinning checks each logical redirect while
// preserving Host, path and query. No DNS name is passed to the socket dialer.
func TestURLFetchSecurityRedirectAndPinning(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, target := range []string{"/ok?value=%3Cscript%3E", "http://100.100.100.200/metadata"} {
		t.Run(target, func(t *testing.T) {
			seenHosts := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenHosts <- r.Host
				if r.URL.Path == "/start" {
					w.Header().Set("Location", target)
					w.WriteHeader(http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = io.WriteString(w, r.URL.RequestURI())
			}))
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			require.NoError(t, err)
			logicalHost := net.JoinHostPort("image.example.test", port)
			destinations := make(chan string, 4)
			fetcher := NewURLFetcher(URLFetchConfig{
				AllowHTTP: true, MaxRedirects: 3, LookupHost: staticResolver(net.ParseIP("1.1.1.1")),
				DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					destinations <- address
					return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
				},
			})
			result, err := fetcher.Fetch(context.Background(), "http://"+logicalHost+"/start")
			expectedCalls := 1
			if strings.HasPrefix(target, "/") {
				require.NoError(t, err)
				require.Equal(t, target, string(result.Body))
				expectedCalls = 2
			} else {
				require.ErrorIs(t, err, ErrURLBlocked)
			}
			require.Len(t, destinations, expectedCalls)
			require.Len(t, seenHosts, expectedCalls)
			for range expectedCalls {
				require.Equal(t, net.JoinHostPort("1.1.1.1", port), <-destinations)
				require.Equal(t, logicalHost, <-seenHosts)
			}
		})
	}
}

// TestURLFetchSecurityRebindingAndCancellation makes DNS public only for the
// first hop. A later private answer is rejected before a second connection.
func TestURLFetchSecurityRebindingAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	var lookups, dials atomic.Int32
	fetcher := NewURLFetcher(URLFetchConfig{
		AllowHTTP: true, MaxRedirects: 3,
		LookupHost: func(context.Context, string) ([]net.IP, error) {
			if lookups.Add(1) == 1 {
				return []net.IP{net.ParseIP("1.1.1.1")}, nil
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	_, err := fetcher.Fetch(context.Background(), "http://image.example.test/first")
	require.ErrorIs(t, err, ErrURLBlocked)
	require.EqualValues(t, 2, lookups.Load())
	require.EqualValues(t, 1, dials.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fetcher.Fetch(ctx, "http://image.example.test/canceled")
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, dials.Load())
}

// TestURLFetchSecurityTLSIdentity uses the production request/transport builder
// and a locally trusted certificate. Trusting the fixture must not bypass the
// original hostname check or change HTTP Host to the numeric dial destination.
func TestURLFetchSecurityTLSIdentity(t *testing.T) {
	seen := make(chan string, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host
		_, _ = io.WriteString(w, "tls-image")
	}))
	defer server.Close()
	require.NotEmpty(t, server.Certificate().DNSNames)
	validName := server.Certificate().DNSNames[0]
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	fetcher := NewURLFetcher(URLFetchConfig{
		LookupHost: staticResolver(net.ParseIP("1.1.1.1")),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	for _, name := range []string{validName, "wrong-certificate.example.test"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target, err := url.Parse("https://" + name + "/image")
			require.NoError(t, err)
			request, transport, err := fetcher.pinnedRequest(ctx, target)
			require.NoError(t, err)
			require.Nil(t, transport.Proxy)
			require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
			require.Equal(t, name, transport.TLSClientConfig.ServerName)
			transport.TLSClientConfig.RootCAs = roots
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport}).Do(request)
			if name != validName {
				require.Error(t, err, "certificate hostname verification remains mandatory")
				return
			}
			require.NoError(t, err)
			body, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			require.NoError(t, readErr)
			require.NoError(t, closeErr)
			require.Equal(t, "tls-image", string(body))
			require.Equal(t, validName, <-seen)
		})
	}
	require.Empty(t, seen, "rejected TLS requests must never reach the HTTP handler")
}

// TestURLFetchSecurityLiteralsAndDeadline checks IPv6 parsing, blocked literal
// addresses independently of DNS hooks, and the deadline while reading a body.
func TestURLFetchSecurityLiteralsAndDeadline(t *testing.T) {
	fetcher := NewURLFetcher(URLFetchConfig{LookupHost: staticResolver(net.ParseIP("1.1.1.1"))})
	for _, value := range []string{"https://127.0.0.1/x", "https://[::ffff:100.100.100.200]/x", "https://[fe80::1%25eth0]/x", "https://user:pass@example.test/x", "https://example.test:0/x"} {
		_, err := fetcher.Fetch(context.Background(), value)
		require.ErrorIs(t, err, ErrURLBlocked)
	}
	target, err := url.Parse("https://[2606:4700:4700::1111]/a%2Fb?q=1")
	require.NoError(t, err)
	request, transport, err := fetcher.pinnedRequest(context.Background(), target)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	require.Equal(t, "[2606:4700:4700::1111]:443", request.URL.Host)
	require.Equal(t, "/a%2Fb?q=1", request.URL.RequestURI())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	fetcher = NewURLFetcher(URLFetchConfig{
		AllowHTTP: true, TotalTimeout: 100 * time.Millisecond,
		LookupHost: staticResolver(net.ParseIP("1.1.1.1")),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	_, err = fetcher.Fetch(context.Background(), "http://image.example.test/body?secret=synthetic-query")
	require.ErrorIs(t, err, ErrURLTimeout)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotContains(t, err.Error(), "synthetic-query")
}
