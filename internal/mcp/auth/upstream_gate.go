package auth

import (
	"context"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/library/clientip"
)

// ErrUpstreamThrottled marks a one-api call this server refused to start
// because one of its own budgets is spent. Such an error also matches
// ErrValidationUnavailable (callers answer 503). Unlike a genuine upstream
// failure, it never grants stale-if-error, so a caller cannot extend a revoked
// key's life by emptying a budget.
var ErrUpstreamThrottled = errors.New("one-api call refused by this server's upstream budget")

// unknownSource groups callers whose client address is unavailable.
const unknownSource = "unknown"

// callClass selects the budgets a one-api call is charged to.
type callClass int

const (
	// callNewKey covers keys without a prior acceptance and balance queries.
	// It is charged to the caller's per-source budget, then the global one.
	callNewKey callClass = iota
	// callRevalidation covers keys that one-api accepted before and whose
	// trust period ended. It is charged to the reserved revalidation budget,
	// which unknown-key traffic cannot spend, and falls back to callNewKey's
	// budgets when the reserve is empty.
	callRevalidation
)

// UpstreamGate is the single admission point for this server's calls to
// one-api's per-IP-limited REST API: API key validation, revalidation and the
// balance proxies. It holds the per-source budgets, the reserved revalidation
// budget, the global budget and the concurrency cap.
type UpstreamGate struct {
	now          func() time.Time
	sources      *sourceBudgets
	global       *rate.Limiter
	revalidation *rate.Limiter
	slots        chan struct{}
}

// newUpstreamGate builds the gate from normalized options.
func newUpstreamGate(opts AuthenticatorOptions) *UpstreamGate {
	gate := &UpstreamGate{
		now:     opts.Now,
		sources: newSourceBudgets(opts.PerSourceRatePerSecond, opts.PerSourceBurst, opts.MaxSources),
		global:  rate.NewLimiter(rate.Limit(opts.UpstreamRatePerSecond), opts.UpstreamBurst),
		slots:   make(chan struct{}, opts.MaxConcurrent),
	}
	if opts.RevalidationRatePerSecond > 0 {
		gate.revalidation = rate.NewLimiter(rate.Limit(opts.RevalidationRatePerSecond), opts.RevalidationBurst)
	}
	return gate
}

// admit charges one upstream call on behalf of the client source recorded on
// ctx and waits, bounded by ctx, for a concurrency slot. The returned release
// frees the slot. Every refusal matches ErrUpstreamThrottled.
func (g *UpstreamGate) admit(ctx context.Context, class callClass) (func(), error) {
	now := g.now()
	if !g.charge(sourceKey(clientip.FromContext(ctx)), class, now) {
		return nil, throttled(nil, "one-api call budget spent")
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, throttled(ctx.Err(), "wait for a one-api call slot")
	}
}

// charge takes one token from the budgets of class at now.
func (g *UpstreamGate) charge(source string, class callClass, now time.Time) bool {
	if class == callRevalidation && g.revalidation != nil && g.revalidation.AllowN(now, 1) {
		return true
	}
	reservation, ok := g.sources.reserve(source, now)
	if !ok {
		return false
	}
	if !g.global.AllowN(now, 1) {
		// The source did not cause this refusal, so it keeps its token.
		reservation.CancelAt(now)
		return false
	}
	return true
}

// throttled builds an ErrUpstreamThrottled error that also maps to 503.
func throttled(cause error, reason string) error {
	return errors.Wrap(errors.Join(ErrValidationUnavailable, ErrUpstreamThrottled, cause), reason)
}

// sourceKey maps a client address to its budget source with the shared client
// identity rule (clientip.NetworkKey): an IPv4 address, or an IPv6 /64.
// Missing or unusable addresses share one "unknown" source.
func sourceKey(address string) string {
	if key := clientip.NetworkKey(address); key != "" {
		return key
	}
	return unknownSource
}

// sourceBudgets bounds upstream calls per source, so one source cannot drain
// the global budget that protects one-api. Sources live in a bounded LRU; an
// idle entry expires once its bucket would have refilled anyway.
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
// without charging, when the source is over its budget. A successful
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

// EnsureClientIP keeps the client address the request-identity middleware
// recorded on ctx and otherwise records the TCP peer of remoteAddr, for
// handlers served outside the gin engine.
func EnsureClientIP(ctx context.Context, remoteAddr string) context.Context {
	if clientip.FromContext(ctx) != "" {
		return ctx
	}
	return clientip.WithIP(ctx, clientip.PeerAddress(remoteAddr))
}
