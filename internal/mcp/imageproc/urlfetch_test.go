package imageproc

import (
	"context"
	"fmt"
	"image/color"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// staticResolver returns a LookupHost stub that always yields ips.
func staticResolver(ips ...net.IP) func(ctx context.Context, host string) ([]net.IP, error) {
	return func(_ context.Context, _ string) ([]net.IP, error) {
		return append([]net.IP{}, ips...), nil
	}
}

func TestFetchHTTPBlockedByDefault(t *testing.T) {
	f := NewURLFetcher(URLFetchConfig{
		LookupHost: staticResolver(net.ParseIP("1.1.1.1")),
	})
	_, err := f.Fetch(context.Background(), "http://example.com/a.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLBlocked))
}

func TestFetchLoopbackRejected(t *testing.T) {
	f := NewURLFetcher(URLFetchConfig{
		LookupHost: staticResolver(net.ParseIP("127.0.0.1")),
	})
	_, err := f.Fetch(context.Background(), "https://example.com/a.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLBlocked))
}

func TestFetchPrivateIPRejected(t *testing.T) {
	f := NewURLFetcher(URLFetchConfig{
		LookupHost: staticResolver(net.ParseIP("10.0.0.1")),
	})
	_, err := f.Fetch(context.Background(), "https://example.com/a.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLBlocked))
}

func TestFetchMetadataIPRejected(t *testing.T) {
	f := NewURLFetcher(URLFetchConfig{
		LookupHost: staticResolver(net.ParseIP("169.254.169.254")),
	})
	_, err := f.Fetch(context.Background(), "https://example.com/a.png")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLBlocked))
}

func TestFetchDisallowedScheme(t *testing.T) {
	f := NewURLFetcher(URLFetchConfig{LookupHost: staticResolver(net.ParseIP("1.1.1.1"))})
	_, err := f.Fetch(context.Background(), "file:///etc/passwd")
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLBlocked))
}

func TestFetchSuccessAgainstLocalServer(t *testing.T) {
	raw := makePNG(t, 20, 20, color.RGBA{R: 5, G: 5, B: 5, A: 255})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	cfg := URLFetchConfig{
		AllowHTTP:    true,
		LookupHost:   staticResolver(net.ParseIP("127.0.0.1")),
		DialContext:  (&net.Dialer{}).DialContext,
		MaxBodyBytes: 1 << 20,
		TotalTimeout: 5 * time.Second,
	}
	// Scheme validation still refuses loopback IPs; bypass the DNS guard by
	// using a resolver that returns a public IP and then dialing directly to
	// the test server via DialContext.
	cfg.LookupHost = staticResolver(net.ParseIP("1.1.1.1"))
	cfg.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	f := NewURLFetcher(cfg)
	res, err := f.Fetch(context.Background(), strings.Replace(srv.URL, "http://127.0.0.1", "http://example.com", 1))
	require.NoError(t, err)
	require.Equal(t, raw, res.Body)
	require.Equal(t, "image/png", res.MIMEHint)
}

func TestFetchBodyCap(t *testing.T) {
	// Server streams more than the cap — body should truncate and return ErrImageTooLarge.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		huge := make([]byte, 2<<20) // 2 MiB
		_, _ = w.Write(huge)
	}))
	defer srv.Close()

	cfg := URLFetchConfig{
		AllowHTTP:    true,
		LookupHost:   staticResolver(net.ParseIP("1.1.1.1")),
		MaxBodyBytes: 1024,
		TotalTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	f := NewURLFetcher(cfg)
	_, err := f.Fetch(context.Background(), strings.Replace(srv.URL, "http://127.0.0.1", "http://example.com", 1))
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrImageTooLarge))
}

func TestFetchTooManyRedirects(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Location", fmt.Sprintf("/step?n=%d", count.Load()))
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	cfg := URLFetchConfig{
		AllowHTTP:    true,
		MaxRedirects: 3,
		LookupHost:   staticResolver(net.ParseIP("1.1.1.1")),
		MaxBodyBytes: 1024,
		TotalTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	f := NewURLFetcher(cfg)
	_, err := f.Fetch(context.Background(), strings.Replace(srv.URL, "http://127.0.0.1", "http://example.com", 1))
	require.Error(t, err)
	require.True(t, errs.Is(err, ErrURLFetchFailed))
}
