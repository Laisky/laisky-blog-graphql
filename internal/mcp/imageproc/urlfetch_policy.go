package imageproc

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	errors "github.com/Laisky/errors/v2"
)

const imageURLSchemeHTTP = "http"

// Special-purpose, documentation and transition ranges are not image origins.
// In particular, IsPrivate does not include shared space (100.64.0.0/10), which
// contains Alibaba's metadata endpoint. Keep IPv4-mapped IPv6 checks identical.
var blockedFetchPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var nativeIPv6Unicast = netip.MustParsePrefix("2000::/3")

// isPublicIP limits fetches to ordinary public unicast destinations. Transition
// mechanisms such as NAT64/6to4 must not encode an otherwise blocked IPv4 target.
func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	if addr.Is6() && !nativeIPv6Unicast.Contains(addr) {
		return false
	}
	for _, prefix := range blockedFetchPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// publicEndpoint validates the logical origin and resolves it exactly once. The
// returned authority is numeric, so neither the HTTP transport nor a later DNS
// answer can select a different destination. Every DNS answer must be public.
func (f *URLFetcher) publicEndpoint(ctx context.Context, target *url.URL) (string, error) {
	if target.Opaque != "" || target.User != nil || target.Hostname() == "" || strings.HasSuffix(target.Host, ":") {
		return "", errors.Wrap(ErrURLBlocked, "invalid image origin")
	}
	port := target.Port()
	switch target.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case imageURLSchemeHTTP:
		if !f.cfg.AllowHTTP {
			return "", errors.Wrap(ErrURLBlocked, "HTTP image origins are disabled")
		}
		if port == "" {
			port = "80"
		}
	default:
		return "", errors.Wrap(ErrURLBlocked, "unsupported image origin scheme")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", errors.Wrap(ErrURLBlocked, "invalid image origin port")
	}
	ips, err := f.resolveOrigin(ctx, target.Hostname())
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", errors.Wrap(ErrURLBlocked, "image origin has no addresses")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return "", errors.Wrap(ErrURLBlocked, "image origin resolves to a non-public address")
		}
	}
	return net.JoinHostPort(ips[0].String(), strconv.FormatUint(portNumber, 10)), nil
}

// resolveOrigin validates literals without consulting DNS, including IPv6 zones.
func (f *URLFetcher) resolveOrigin(ctx context.Context, host string) ([]net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return nil, errors.Wrap(ErrURLBlocked, "scoped image origins are not allowed")
		}
		return []net.IP{net.IP(addr.Unmap().AsSlice())}, nil
	}
	ips, err := f.cfg.LookupHost(ctx, host)
	if err != nil {
		return nil, errors.Wrap(err, "resolve image origin")
	}
	return ips, nil
}
