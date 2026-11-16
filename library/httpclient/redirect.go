// Package httpclient holds outbound HTTP client policies shared by callers
// that send credentials to an upstream service.
package httpclient

import "net/http"

// WithoutRedirects returns a copy of base that never follows redirects: the
// 3xx response itself is returned to the caller, with its body unclosed. A nil
// base yields a zero-value client (http.DefaultTransport, no timeout). base is
// never modified; the copy shares its Transport (and connection pool), Jar and
// Timeout.
//
// Use it for every request that carries a caller's credential, so the
// credential is only ever sent to the URL the caller built. Go's own redirect
// policy strips Authorization only when the target host is neither the
// original host nor one of its subdomains: it keeps the header on a subdomain,
// on a port change and on an https→http downgrade of the same host.
func WithoutRedirects(base *http.Client) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	}
	client.CheckRedirect = refuseRedirect
	return &client
}

// refuseRedirect stops at the first redirect response.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
