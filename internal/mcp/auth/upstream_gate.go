package auth

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	errors "github.com/Laisky/errors/v2"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
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

// GatewaySource gives one client address range its own per-source budget:
// a legitimate gateway or NAT that serves many users from few addresses.
// Matching uses the client address the trusted-proxy policy resolved
// (library/clientip), so a forwarding header from an untrusted peer can never
// select a gateway's budget. All addresses of the range share one budget,
// which stays within the global budget.
type GatewaySource struct {
	// Prefix is the gateway's address range, e.g. 203.0.113.10/32.
	Prefix netip.Prefix
	// RatePerSecond and Burst replace the per-source defaults for the range.
	RatePerSecond float64
	Burst         int
}

// UpstreamGate is the single admission point for this server's calls to
// one-api's per-IP-limited REST API made on behalf of a presented key: API
// key validation, revalidation and the balance proxies. It holds the
// per-source budgets, the reserved revalidation budget, the global budget,
// the concurrency cap and, when configured, one-api's own per-IP window,
// which it shares with every other one-api REST call of the process (the
// consumes, recorded in oneapi.CallLedger).
type UpstreamGate struct {
	// charging serializes charge: golang.org/x/time/rate cannot fully
	// refund a canceled reservation once a later reservation landed, so two
	// refused calls interleaving reserve and cancel would leave a bucket in
	// debt. Holding it makes every refused charge refund exactly.
	charging     sync.Mutex
	now          func() time.Time
	sources      *sourceBudgets
	global       *rate.Limiter
	revalidation *rate.Limiter
	slots        chan struct{}
	// ledger and windowLimit bound this process's one-api REST calls per
	// oneapi.RateLimitWindow; a nil ledger or a limit of 0 does not bound.
	ledger      *oneapi.CallLedger
	windowLimit int
}

// newUpstreamGate builds the gate from normalized options.
func newUpstreamGate(opts AuthenticatorOptions) *UpstreamGate {
	gate := &UpstreamGate{
		now:         opts.Now,
		sources:     newSourceBudgets(opts.PerSourceRatePerSecond, opts.PerSourceBurst, opts.MaxSources, opts.Gateways),
		global:      rate.NewLimiter(rate.Limit(opts.UpstreamRatePerSecond), opts.UpstreamBurst),
		slots:       make(chan struct{}, opts.MaxConcurrent),
		ledger:      opts.Ledger,
		windowLimit: opts.OneAPIRateLimit,
	}
	if opts.RevalidationRatePerSecond > 0 {
		gate.revalidation = rate.NewLimiter(rate.Limit(opts.RevalidationRatePerSecond), opts.RevalidationBurst)
	}
	return gate
}

// admit waits, bounded by ctx, for a concurrency slot and then charges one
// upstream call on behalf of the client source recorded on ctx. Budgets are
// charged only once the call holds a slot and is about to start, so a caller
// that times out waiting never spends a token (golang.org/x/time/rate cannot
// refund a token after other callers reserved). A budget already spent is
// refused at once, without waiting. The returned release frees the slot.
// Every refusal matches ErrUpstreamThrottled.
func (g *UpstreamGate) admit(ctx context.Context, class callClass) (func(), error) {
	source := g.sources.resolve(ctx)
	if budget := g.refusal(source, class, g.now()); budget != "" {
		observeGateRefusal(budget)
		return nil, throttled(nil, "one-api call budget spent: "+budget)
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		observeGateRefusal(refusalConcurrency)
		return nil, throttled(ctx.Err(), "wait for a one-api call slot")
	}
	release := func() { <-g.slots }
	if budget := g.charge(source, class, g.now()); budget != "" {
		// Another caller took the last token while this one waited.
		release()
		observeGateRefusal(budget)
		return nil, throttled(nil, "one-api call budget spent: "+budget)
	}
	return release, nil
}

// refusal reports, without charging, which budget would refuse one call of
// class from source at now, or "" when the call could start.
func (g *UpstreamGate) refusal(source sourceBudget, class callClass, now time.Time) string {
	if !g.ledger.Available(now, g.windowLimit) {
		return refusalWindow
	}
	if class == callRevalidation && g.revalidation != nil && g.revalidation.TokensAt(now) >= 1 {
		return ""
	}
	if !g.sources.available(source, now) {
		return refusalSource
	}
	if g.global.TokensAt(now) < 1 {
		return refusalGlobal
	}
	return ""
}

// charge takes one token from the budgets of class at now and counts the
// call in the one-api window. It returns the refusing budget, or "" once the
// call is charged. A refused call keeps every token it had reserved.
func (g *UpstreamGate) charge(source sourceBudget, class callClass, now time.Time) string {
	g.charging.Lock()
	defer g.charging.Unlock()
	if class == callRevalidation && g.revalidation != nil {
		if reservation, ok := reserveNow(g.revalidation, now); ok {
			if g.ledger.TryRecord(now, oneapi.RouteTokenBalance, g.windowLimit) {
				return ""
			}
			reservation.CancelAt(now)
			return refusalWindow
		}
	}
	sourceReservation, ok := g.sources.reserve(source, now)
	if !ok {
		return refusalSource
	}
	globalReservation, ok := reserveNow(g.global, now)
	if !ok {
		// The source did not cause this refusal, so it keeps its token.
		sourceReservation.CancelAt(now)
		return refusalGlobal
	}
	if !g.ledger.TryRecord(now, oneapi.RouteTokenBalance, g.windowLimit) {
		globalReservation.CancelAt(now)
		sourceReservation.CancelAt(now)
		return refusalWindow
	}
	return ""
}

// reserveNow takes one token from limiter at now, or none when the limiter
// would make the call wait.
func reserveNow(limiter *rate.Limiter, now time.Time) (*rate.Reservation, bool) {
	reservation := limiter.ReserveN(now, 1)
	if !reservation.OK() || reservation.DelayFrom(now) > 0 {
		reservation.CancelAt(now)
		return nil, false
	}
	return reservation, true
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

// budgetSourceKey is the context key of a named budget source.
type budgetSourceKey struct{}

// WithBudgetSource charges the one-api calls made for ctx to the named
// source instead of a client address, for callers that have none (a
// Telegram chat: "telegram:<uid>"). The name comes from server code, never
// from request data, and gets the default per-source budget.
func WithBudgetSource(ctx context.Context, name string) context.Context {
	name = strings.TrimSpace(name)
	if ctx == nil || name == "" {
		return ctx
	}
	return context.WithValue(ctx, budgetSourceKey{}, name)
}

// sourceLimit is the budget of one source.
type sourceLimit struct {
	rate  rate.Limit
	burst int
	// idleTTL drops an idle bucket once it would have refilled anyway.
	idleTTL time.Duration
}

// newSourceLimit builds a source budget refilled at ratePerSecond.
func newSourceLimit(ratePerSecond float64, burst int) sourceLimit {
	idle := time.Duration(float64(burst) / ratePerSecond * float64(time.Second))
	return sourceLimit{rate: rate.Limit(ratePerSecond), burst: burst, idleTTL: max(idle, time.Second)}
}

// sourceBudget names the bucket a call is charged to and its budget.
type sourceBudget struct {
	key   string
	limit sourceLimit
}

// gatewayBudget is a configured gateway range and its budget.
type gatewayBudget struct {
	prefix netip.Prefix
	source sourceBudget
}

// sourceBudgets bounds upstream calls per source, so one source cannot drain
// the global budget that protects one-api. Sources live in a bounded LRU; an
// idle entry expires once its bucket would have refilled anyway.
type sourceBudgets struct {
	mu       sync.Mutex
	defaults sourceLimit
	gateways []gatewayBudget
	buckets  *ttlCache[*rate.Limiter]
}

// newSourceBudgets builds the per-source buckets.
func newSourceBudgets(ratePerSecond float64, burst, maxSources int, gateways []GatewaySource) *sourceBudgets {
	budgets := &sourceBudgets{
		defaults: newSourceLimit(ratePerSecond, burst),
		buckets:  newTTLCache[*rate.Limiter](maxSources),
	}
	for _, gateway := range gateways {
		prefix := gateway.Prefix.Masked()
		budgets.gateways = append(budgets.gateways, gatewayBudget{
			prefix: prefix,
			source: sourceBudget{key: "gateway:" + prefix.String(), limit: newSourceLimit(gateway.RatePerSecond, gateway.Burst)},
		})
	}
	return budgets
}

// resolve returns the source of the calls made for ctx: a named source, a
// configured gateway containing the client address, or the address's
// network key.
func (b *sourceBudgets) resolve(ctx context.Context) sourceBudget {
	if name, ok := ctx.Value(budgetSourceKey{}).(string); ok && name != "" {
		return sourceBudget{key: "named:" + name, limit: b.defaults}
	}
	address := clientip.FromContext(ctx)
	if parsed, err := netip.ParseAddr(strings.TrimSpace(address)); err == nil {
		parsed = parsed.Unmap()
		for _, gateway := range b.gateways {
			if gateway.prefix.Contains(parsed) {
				return gateway.source
			}
		}
	}
	return sourceBudget{key: sourceKey(address), limit: b.defaults}
}

// available reports whether the source's bucket holds a token at now,
// without charging it or tracking a new source.
func (b *sourceBudgets) available(source sourceBudget, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	limiter, ok := b.buckets.get(source.key, now)
	return !ok || limiter.TokensAt(now) >= 1
}

// reserve takes one token from the source's bucket at now. It returns false,
// without charging, when the source is over its budget. A successful
// reservation can be canceled if a later budget then refuses the call.
func (b *sourceBudgets) reserve(source sourceBudget, now time.Time) (*rate.Reservation, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	limiter, ok := b.buckets.get(source.key, now)
	if !ok {
		limiter = rate.NewLimiter(source.limit.rate, source.limit.burst)
	}
	b.buckets.put(source.key, limiter, now.Add(source.limit.idleTTL))
	return reserveNow(limiter, now)
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
