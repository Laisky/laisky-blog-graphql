package imageproc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	laiskyerr "github.com/Laisky/errors/v2"
)

// URLFetchConfig tunes the SSRF-guarded fetcher.
type URLFetchConfig struct {
	// AllowHTTP allows unencrypted http:// URLs. Default: false.
	AllowHTTP bool
	// MaxRedirects caps HTTP redirect depth. Zero means no following.
	MaxRedirects int
	// TotalTimeout is the overall deadline for the request (headers + body).
	TotalTimeout time.Duration
	// TLSHandshakeTimeout bounds the TLS handshake.
	TLSHandshakeTimeout time.Duration
	// ResponseHeaderTimeout bounds the wait for response headers after the
	// request is sent.
	ResponseHeaderTimeout time.Duration
	// MaxBodyBytes is the hard cap on the response body size.
	MaxBodyBytes int64
	// LookupHost resolves a hostname to IPs. Defaults to net.DefaultResolver.
	// Tests override this to simulate DNS-rebinding and private-IP scenarios.
	LookupHost func(ctx context.Context, host string) ([]net.IP, error)
	// DialContext is the transport-level dialer. Defaults to a net.Dialer
	// that re-checks the resolved IP at connect time. Tests typically leave
	// this nil to use the production behavior.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DefaultURLFetchConfig returns a conservative configuration.
func DefaultURLFetchConfig() URLFetchConfig {
	return URLFetchConfig{
		AllowHTTP:             false,
		MaxRedirects:          3,
		TotalTimeout:          15 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxBodyBytes:          20 * 1024 * 1024,
	}
}

// FetchResult is the output of Fetch: raw bytes + a MIME hint from the server.
type FetchResult struct {
	Body     []byte
	MIMEHint string
}

// URLFetcher implements the §3.7 SSRF-guarded fetcher.
type URLFetcher struct {
	cfg URLFetchConfig
}

// NewURLFetcher constructs a URLFetcher with the provided configuration. If
// LookupHost is nil it uses net.DefaultResolver.
func NewURLFetcher(cfg URLFetchConfig) *URLFetcher {
	if cfg.LookupHost == nil {
		cfg.LookupHost = defaultLookup
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 15 * time.Second
	}
	if cfg.TLSHandshakeTimeout <= 0 {
		cfg.TLSHandshakeTimeout = 5 * time.Second
	}
	if cfg.ResponseHeaderTimeout <= 0 {
		cfg.ResponseHeaderTimeout = 10 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 20 * 1024 * 1024
	}
	return &URLFetcher{cfg: cfg}
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// Fetch downloads the URL and returns the body bytes plus the server-declared
// Content-Type. All guards described in the proposal §3.7 are applied.
func (f *URLFetcher) Fetch(ctx context.Context, url string) (FetchResult, error) {
	if err := f.validateURL(ctx, url); err != nil {
		return FetchResult{}, err
	}

	dial := f.cfg.DialContext
	if dial == nil {
		dial = f.safeDialContext()
	}
	transport := &http.Transport{
		DialContext:           dial,
		TLSHandshakeTimeout:   f.cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: f.cfg.ResponseHeaderTimeout,
		DisableKeepAlives:     true,
	}

	// redirectsLeft is captured by CheckRedirect to enforce the hop cap.
	redirectsLeft := f.cfg.MaxRedirects
	client := &http.Client{
		Transport: transport,
		Timeout:   f.cfg.TotalTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > redirectsLeft {
				return laiskyerr.Wrap(ErrURLFetchFailed, "too many redirects")
			}
			if err := f.validateURL(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, laiskyerr.Wrap(ErrURLFetchFailed, err.Error())
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", "laisky-mcp-image-fetcher/1.0")

	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return FetchResult{}, laiskyerr.Wrap(ErrURLTimeout, err.Error())
		}
		// Preserve the sentinel wrapped by validateURL/CheckRedirect.
		if errors.Is(err, ErrURLBlocked) || errors.Is(err, ErrURLFetchFailed) || errors.Is(err, ErrURLTimeout) {
			return FetchResult{}, err
		}
		return FetchResult{}, laiskyerr.Wrap(ErrURLFetchFailed, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FetchResult{}, laiskyerr.Wrapf(ErrURLFetchFailed, "status %d", resp.StatusCode)
	}

	body, err := readAllCapped(resp.Body, f.cfg.MaxBodyBytes)
	if err != nil {
		if laiskyerr.Is(err, ErrImageTooLarge) {
			return FetchResult{}, err
		}
		if isTimeout(err) {
			return FetchResult{}, laiskyerr.Wrap(ErrURLTimeout, err.Error())
		}
		return FetchResult{}, laiskyerr.Wrap(ErrURLFetchFailed, err.Error())
	}

	return FetchResult{
		Body:     body,
		MIMEHint: strings.ToLower(resp.Header.Get("Content-Type")),
	}, nil
}

// safeDialContext returns a DialContext that re-resolves the destination host
// at connect time and rejects any private / loopback / metadata destination.
func (f *URLFetcher) safeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: f.cfg.TLSHandshakeTimeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return nil, laiskyerr.Wrap(ErrURLBlocked, splitErr.Error())
		}
		ips, err := f.cfg.LookupHost(ctx, host)
		if err != nil {
			return nil, laiskyerr.Wrap(ErrURLBlocked, err.Error())
		}
		for _, ip := range ips {
			if !isPublicIP(ip) {
				return nil, laiskyerr.Wrapf(ErrURLBlocked, "private IP %s", ip.String())
			}
		}
		// Pick the first public IP.
		if len(ips) == 0 {
			return nil, laiskyerr.Wrap(ErrURLBlocked, "no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// validateURL enforces the scheme allowlist and runs a DNS guard.
func (f *URLFetcher) validateURL(ctx context.Context, raw string) error {
	parsed, err := urlParse(raw)
	if err != nil {
		return laiskyerr.Wrap(ErrURLBlocked, err.Error())
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !f.cfg.AllowHTTP {
			return laiskyerr.Wrapf(ErrURLBlocked, "scheme %q disabled", scheme)
		}
	default:
		return laiskyerr.Wrapf(ErrURLBlocked, "scheme %q disallowed", scheme)
	}
	host := parsed.Host
	if idx := strings.IndexRune(host, ':'); idx >= 0 {
		host = host[:idx]
	}
	if host == "" {
		return laiskyerr.Wrap(ErrURLBlocked, "missing host")
	}
	ips, err := f.cfg.LookupHost(ctx, host)
	if err != nil {
		return laiskyerr.Wrap(ErrURLBlocked, err.Error())
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return laiskyerr.Wrapf(ErrURLBlocked, "private IP %s", ip.String())
		}
	}
	return nil
}

// isPublicIP rejects all private / reserved / metadata IP ranges.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// Block AWS / GCP / Azure metadata endpoints explicitly (covered by
	// link-local, but enforced for defense-in-depth).
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return false
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return false
	}
	// Block IPv4-mapped IPv6 for private ranges.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return true
}

// urlParse wraps net/url.Parse + validation to avoid the import cycle hazard
// of pulling net/url into this file twice.
func urlParse(raw string) (parsedURL, error) {
	u, err := netURLParse(raw)
	if err != nil {
		return parsedURL{}, err
	}
	return parsedURL{Scheme: u.Scheme, Host: u.Host}, nil
}

type parsedURL struct {
	Scheme string
	Host   string
}
