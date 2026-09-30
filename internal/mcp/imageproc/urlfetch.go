package imageproc

import (
	"context"
	"crypto/tls"
	stderrors "errors"
	"net"
	"net/http"
	"net/url"
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
	// DialContext is a trusted transport hook, normally nil. It receives only
	// the validated numeric destination, never the untrusted origin hostname.
	// Tests may redirect that connection to a local fixture.
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
		return nil, laiskyerr.Wrap(err, "resolve image origin")
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// Fetch downloads an image through public, pinned destinations. Every redirect
// is validated independently, with one deadline covering DNS, all hops and body.
func (f *URLFetcher) Fetch(ctx context.Context, rawURL string) (FetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, f.cfg.TotalTimeout)
	defer cancel()
	target, err := url.Parse(rawURL)
	if err != nil {
		return FetchResult{}, laiskyerr.Wrap(ErrURLBlocked, "invalid image URL")
	}
	for hops := 0; ; hops++ {
		response, err := f.fetchHop(ctx, target)
		if err != nil {
			return FetchResult{}, classifyFetchError(err)
		}
		location := response.Header.Get("Location")
		if isImageRedirect(response.StatusCode) && location != "" {
			if err := response.Body.Close(); err != nil {
				return FetchResult{}, classifyFetchError(err)
			}
			if hops >= f.cfg.MaxRedirects {
				return FetchResult{}, laiskyerr.Wrap(ErrURLFetchFailed, "too many redirects")
			}
			// Resolve relative locations against the logical hostname, not the
			// numeric connection target. The next hop repeats all origin checks.
			target, err = target.Parse(location)
			if err != nil {
				return FetchResult{}, laiskyerr.Wrap(ErrURLBlocked, "invalid image redirect")
			}
			continue
		}
		return f.readResponse(response)
	}
}

// pinnedRequest retains HTTP Host and TLS certificate/SNI identity while using
// only a validated numeric authority for the network connection. Environment
// proxies are intentionally not used: they would re-resolve the logical host.
func (f *URLFetcher) pinnedRequest(ctx context.Context, target *url.URL) (*http.Request, *http.Transport, error) {
	endpoint, err := f.publicEndpoint(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	scheme := "https"
	if target.Scheme == imageURLSchemeHTTP {
		scheme = imageURLSchemeHTTP
	}
	wireURL := url.URL{
		Scheme: scheme, Host: endpoint,
		Path: target.Path, RawPath: target.RawPath,
		RawQuery: target.RawQuery, ForceQuery: target.ForceQuery,
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, wireURL.String(), nil)
	if err != nil {
		return nil, nil, laiskyerr.Wrap(ErrURLBlocked, "invalid image request")
	}
	request.Host = target.Host
	request.Header.Set("Accept", "image/*")
	request.Header.Set("User-Agent", "laisky-mcp-image-fetcher/1.0")
	dial := f.cfg.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: f.cfg.TLSHandshakeTimeout}).DialContext
	}
	transport := &http.Transport{
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{ServerName: target.Hostname(), MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   f.cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: f.cfg.ResponseHeaderTimeout,
		DisableKeepAlives:     true,
	}
	return request, transport, nil
}

// fetchHop never follows a redirect inside the HTTP client; Fetch must validate
// the next logical origin before another connection can be opened.
func (f *URLFetcher) fetchHop(ctx context.Context, target *url.URL) (*http.Response, error) {
	request, transport, err := f.pinnedRequest(ctx, target)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, classifyFetchError(err)
	}
	return response, nil
}

// readResponse always closes the body and preserves the primary read failure.
func (f *URLFetcher) readResponse(response *http.Response) (FetchResult, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		closeErr := response.Body.Close()
		return FetchResult{}, laiskyerr.Wrapf(stderrors.Join(ErrURLFetchFailed, closeErr), "image status %d", response.StatusCode)
	}
	body, err := readAllCapped(response.Body, f.cfg.MaxBodyBytes)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return FetchResult{}, classifyFetchError(stderrors.Join(err, closeErr))
	}
	return FetchResult{Body: body, MIMEHint: strings.ToLower(response.Header.Get("Content-Type"))}, nil
}

// isImageRedirect matches the redirect statuses followed by net/http for GET.
func isImageRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// classifyFetchError preserves cancellation and policy sentinels without
// retaining a URL (which may contain private paths or query tokens) in errors.
func classifyFetchError(err error) error {
	var urlErr *url.Error
	if stderrors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if isTimeout(err) {
		return laiskyerr.Wrap(stderrors.Join(ErrURLTimeout, err), "image request timed out")
	}
	if stderrors.Is(err, ErrURLBlocked) || stderrors.Is(err, ErrImageTooLarge) || stderrors.Is(err, ErrURLFetchFailed) {
		return laiskyerr.WithStack(err)
	}
	return laiskyerr.Wrap(stderrors.Join(ErrURLFetchFailed, err), "image request failed")
}
