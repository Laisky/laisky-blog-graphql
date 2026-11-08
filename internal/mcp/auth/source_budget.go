package auth

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// unknownSource groups callers whose client address is unavailable.
const unknownSource = "unknown"

// clientAddressKey stores the caller's client address on a request context.
type clientAddressKey struct{}

// WithClientAddress records the caller's client address on ctx. The per-source
// validation budget is keyed on it. Entry points set it once per request; the
// HTTP layer derives it from the request (see WithRequestClientAddress).
func WithClientAddress(ctx context.Context, address string) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, clientAddressKey{}, strings.TrimSpace(address))
}

// ClientAddressFromContext returns the client address recorded on ctx, or "".
func ClientAddressFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	address, _ := ctx.Value(clientAddressKey{}).(string)
	return address
}

// EnsureClientAddress keeps a client address already recorded on ctx by an
// outer layer and otherwise records peer, the request's TCP peer address.
func EnsureClientAddress(ctx context.Context, peer string) context.Context {
	if ClientAddressFromContext(ctx) != "" {
		return ctx
	}
	return WithClientAddress(ctx, peer)
}

// sourceKey maps a client address to its budget source: the IPv4 address, or
// the /64 prefix of an IPv6 address, since one subscriber usually holds a
// whole /64. Unparsable or missing addresses share one "unknown" source.
func sourceKey(address string) string {
	host := strings.TrimSpace(address)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return unknownSource
	}
	ip = ip.WithZone("").Unmap()
	if ip.Is4() {
		return ip.String()
	}
	prefix, err := ip.Prefix(64)
	if err != nil {
		return unknownSource
	}
	return prefix.String()
}

// sourceBudgets bounds upstream validations per source, so one source cannot
// drain the global budget that protects one-api. Sources live in a bounded LRU;
// an idle entry expires once its bucket would have refilled anyway.
type sourceBudgets struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	idleTTL time.Duration
	buckets *ttlCache[*rate.Limiter]
}

// newSourceBudgets builds the per-source buckets.
func newSourceBudgets(ratePerSecond float64, burst, maxSources int) *sourceBudgets {
	idle := time.Duration(float64(burst) / ratePerSecond * float64(time.Second))
	return &sourceBudgets{
		limit:   rate.Limit(ratePerSecond),
		burst:   burst,
		idleTTL: max(idle, time.Second),
		buckets: newTTLCache[*rate.Limiter](maxSources),
	}
}

// reserve takes one token from the source's bucket at now. It returns false,
// without charging, when the source is over its budget; a successful
// reservation can be canceled if the global budget then refuses the call.
func (b *sourceBudgets) reserve(source string, now time.Time) (*rate.Reservation, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	limiter, ok := b.buckets.get(source, now)
	if !ok {
		limiter = rate.NewLimiter(b.limit, b.burst)
	}
	b.buckets.put(source, limiter, now.Add(b.idleTTL))

	reservation := limiter.ReserveN(now, 1)
	if !reservation.OK() || reservation.DelayFrom(now) > 0 {
		reservation.CancelAt(now)
		return nil, false
	}
	return reservation, true
}
