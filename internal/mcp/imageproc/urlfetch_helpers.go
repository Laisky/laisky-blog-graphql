package imageproc

import (
	"net"
	"net/url"
)

// netURLParse is a thin wrapper kept in its own file so urlfetch.go can
// avoid importing net/url alongside the custom parsedURL helper. A separate
// file keeps the core guard logic easy to read and audit.
func netURLParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// isTimeout returns true if err is a network timeout (including unwrapped).
func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	for err != nil {
		if t, ok := err.(timeout); ok && t.Timeout() {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ = net.ParseIP // keep net imported for isPublicIP
