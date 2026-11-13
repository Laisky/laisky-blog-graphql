package clientip

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// HeaderXForwardedHost carries the host the client asked a proxy for.
	HeaderXForwardedHost = "X-Forwarded-Host"
	// HeaderXForwardedProto carries the scheme the client used to reach a proxy.
	HeaderXForwardedProto = "X-Forwarded-Proto"
)

// forwardedKey is the request-context key under which Policy.Attach stores the
// forwarded host and scheme reported by a trusted proxy.
type forwardedKey struct{}

// forwardedRequest is what a trusted proxy reported about the original request.
type forwardedRequest struct {
	host  string
	proto string
}

// TrustsPeer reports whether the connection peer in remoteAddr ("host:port")
// is inside the policy's trusted proxies. It matches what Policy.Apply hands
// to gin, so forwarding headers are believed from exactly the same peers.
func (p Policy) TrustsPeer(remoteAddr string) bool {
	peer := PeerAddress(remoteAddr)
	if peer == "" {
		return false
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	for _, prefix := range p.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Attach resolves the client address like the package-level Attach and, when
// the connection peer is a trusted proxy, also records the X-Forwarded-Host and
// X-Forwarded-Proto it reported. A header from any other peer is ignored, so a
// direct caller cannot choose the host (and with it the site, Turnstile secret
// or redirect origin) the service believes it was asked for.
func (p Policy) Attach(c *gin.Context) string {
	ip := Attach(c)
	if c == nil || c.Request == nil || !p.TrustsPeer(c.Request.RemoteAddr) {
		return ip
	}

	reported := forwardedRequest{
		host:  validForwardedHost(lastListValue(c.Request.Header.Values(HeaderXForwardedHost))),
		proto: validForwardedProto(lastListValue(c.Request.Header.Values(HeaderXForwardedProto))),
	}
	if reported != (forwardedRequest{}) {
		c.Request = c.Request.WithContext(withForwarded(c.Request, reported))
	}
	return ip
}

// Middleware returns a gin middleware that calls Policy.Attach for every request.
func (p Policy) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		p.Attach(c)
		c.Next()
	}
}

// ForwardedHost returns the X-Forwarded-Host a trusted proxy reported for r,
// or "" when r did not come through a trusted proxy (see Policy.Attach), the
// proxy sent none, or the value is not a plain host[:port].
func ForwardedHost(r *http.Request) string {
	return forwardedFrom(r).host
}

// ForwardedProto returns the X-Forwarded-Proto ("http" or "https") a trusted
// proxy reported for r, or "" when there is none.
func ForwardedProto(r *http.Request) string {
	return forwardedFrom(r).proto
}

// RequestHost returns the host the client asked for: the trusted proxy's
// X-Forwarded-Host when present, otherwise the Host header of r. Neither is
// authenticated, so callers must never let it select whether a security
// control applies, only which configured variant of it applies.
func RequestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host := ForwardedHost(r); host != "" {
		return host
	}
	return strings.TrimSpace(r.Host)
}

// withForwarded returns r's context carrying the trusted proxy's report.
func withForwarded(r *http.Request, reported forwardedRequest) context.Context {
	return context.WithValue(r.Context(), forwardedKey{}, reported)
}

// forwardedFrom returns the report stored by Policy.Attach, if any.
func forwardedFrom(r *http.Request) forwardedRequest {
	if r == nil {
		return forwardedRequest{}
	}
	reported, _ := r.Context().Value(forwardedKey{}).(forwardedRequest)
	return reported
}

// lastListValue joins every header line and returns the rightmost non-empty
// comma-separated element: the one added by the proxy closest to this service.
func lastListValue(values []string) string {
	items := strings.Split(strings.Join(values, ","), ",")
	for i := len(items) - 1; i >= 0; i-- {
		if item := strings.TrimSpace(items[i]); item != "" {
			return item
		}
	}
	return ""
}

// validForwardedHost returns host when it is a plain host or host:port made of
// letters, digits, dots, hyphens, colons and IPv6 brackets, and "" otherwise,
// so a reported host can never inject a path, userinfo or another scheme.
func validForwardedHost(host string) string {
	if host == "" || len(host) > 255 {
		return ""
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == ':', r == '[', r == ']':
		default:
			return ""
		}
	}
	return host
}

// validForwardedProto returns "http" or "https" for those schemes (any case)
// and "" for anything else.
func validForwardedProto(proto string) string {
	switch strings.ToLower(proto) {
	case "http":
		return "http"
	case "https":
		return "https"
	default:
		return ""
	}
}
