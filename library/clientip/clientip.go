package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/gin-gonic/gin"
)

// ipv6NetworkBits is the IPv6 prefix length treated as one client by
// NetworkKey. RFC 7934 recommends assigning a whole /64 to a single host, so
// a host can rotate through its /64 at will.
const ipv6NetworkBits = 64

// ginContextKey is the gin.Context key under which Attach stores the address.
const ginContextKey = "laisky-blog-graphql/clientip"

// contextKey is the request-context key under which Attach stores the address.
type contextKey struct{}

// Attach resolves the request's client address with the engine's
// trusted-proxy policy (see Policy.Apply) and records it in both the gin
// context and the request context, so gin handlers, GraphQL resolvers and
// net/http handlers mounted in gin (the MCP transport) read one identity. It
// returns the canonical address, or "" when the peer address is unusable.
func Attach(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}

	ip := Canonical(c.ClientIP())
	c.Set(ginContextKey, ip)
	c.Request = c.Request.WithContext(WithIP(c.Request.Context(), ip))
	return ip
}

// Middleware returns a gin middleware that calls Attach for every request.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		Attach(c)
		c.Next()
	}
}

// WithIP returns a child of ctx that carries ip as the resolved client address.
func WithIP(ctx context.Context, ip string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, ip)
}

// FromContext returns the client address resolved for the request ctx belongs
// to. When the request never passed through Attach it falls back to the
// connection peer, never to a forwarding header, so a missing middleware can
// only make identity coarser, not spoofable. It returns "" when no request is
// reachable from ctx or its peer address is unusable.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if ip, ok := ctx.Value(contextKey{}).(string); ok {
		return ip
	}

	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok || gctx == nil {
		return ""
	}
	if value, exists := gctx.Get(ginContextKey); exists {
		if ip, isString := value.(string); isString {
			return ip
		}
	}

	return FromRequest(gctx.Request)
}

// FromRequest returns the client address Attach resolved for r, or the
// connection peer when r did not pass through it.
func FromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip, ok := r.Context().Value(contextKey{}).(string); ok {
		return ip
	}

	return PeerAddress(r.RemoteAddr)
}

// PeerAddress returns the canonical IP of a net/http RemoteAddr ("host:port"),
// or "" when it holds no IP address.
func PeerAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return ""
	}
	return Canonical(host)
}

// Canonical returns ip in canonical text form: IPv4-mapped IPv6 unmapped, any
// IPv6 zone removed and IPv6 in RFC 5952 lower-case compressed form. It
// returns "" when ip is not an IP address.
func Canonical(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	return addr.WithZone("").Unmap().String()
}

// NetworkKey returns the key under which abuse signals for ip are aggregated:
// an IPv4 address as itself and an IPv6 address as its /64 network, since one
// host may own a whole /64 and rotate through it. It returns "" when ip is not
// an IP address.
func NetworkKey(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}

	prefix, err := addr.Prefix(ipv6NetworkBits)
	if err != nil {
		return ""
	}
	return prefix.String()
}
