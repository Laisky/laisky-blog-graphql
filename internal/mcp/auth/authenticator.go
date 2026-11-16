package auth

import (
	"context"
	"sync/atomic"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"golang.org/x/sync/singleflight"

	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// MaxAPIKeyLength bounds a presented API key. parseAuthorizationHeader and
// DeriveFromAPIKey refuse a longer key before hashing it, so no tenant
// identifier, cache lookup or upstream call ever exists for it. one-api keys,
// including a channel suffix, are far shorter.
const MaxAPIKeyLength = 256

// Validator decides whether an API key is an issued, currently usable credential.
type Validator interface {
	// ValidateAPIKey returns nil for a usable key, an error matching
	// ErrCredentialRejected for a definitive refusal, and any other error when
	// the authority could not decide (outage, timeout, throttling).
	ValidateAPIKey(ctx context.Context, apiKey string) error
}

// ValidatorFunc adapts a plain function to the Validator interface.
type ValidatorFunc func(ctx context.Context, apiKey string) error

// ValidateAPIKey calls the adapted function.
func (f ValidatorFunc) ValidateAPIKey(ctx context.Context, apiKey string) error {
	return f(ctx, apiKey)
}

// InsecureAcceptAllValidator accepts every syntactically valid API key.
// It exists only for an explicit `settings.mcp.auth.validation.enabled: false`
// development opt-out and for tests; production wiring never selects it by default.
type InsecureAcceptAllValidator struct{}

// ValidateAPIKey accepts the key without consulting any authority.
func (InsecureAcceptAllValidator) ValidateAPIKey(context.Context, string) error { return nil }

// AuthenticatorOptions bounds caching and upstream pressure of an Authenticator.
// Zero values take the defaults of DefaultValidationSettings. For StaleGrace
// and RevalidationRatePerSecond a negative value disables the feature.
type AuthenticatorOptions struct {
	// Name labels the validator in logs and wiring checks, e.g. "oneapi".
	Name string
	// PositiveTTL is how long an accepted key is trusted without asking again.
	PositiveTTL time.Duration
	// NegativeTTL is how long a rejected key is refused without asking again.
	NegativeTTL time.Duration
	// StaleGrace extends a previous acceptance only while one-api itself is
	// unavailable; this server's own budget refusals never use it.
	StaleGrace time.Duration
	// Timeout bounds one upstream call, including the wait for a slot.
	Timeout time.Duration
	// MaxEntries bounds each of the key caches.
	MaxEntries int
	// MaxConcurrent bounds simultaneous upstream calls.
	MaxConcurrent int
	// UpstreamRatePerSecond and UpstreamBurst bound upstream calls for new keys
	// and balance queries across all sources.
	UpstreamRatePerSecond float64
	UpstreamBurst         int
	// PerSourceRatePerSecond and PerSourceBurst bound the upstream calls one
	// client source may start, so it cannot drain the global budget.
	PerSourceRatePerSecond float64
	PerSourceBurst         int
	// MaxSources bounds how many client sources are tracked (LRU).
	MaxSources int
	// RevalidationRatePerSecond and RevalidationBurst reserve upstream calls
	// for keys accepted before, which unknown-key traffic cannot spend.
	RevalidationRatePerSecond float64
	RevalidationBurst         int
	// Gateways gives listed client address ranges their own per-source
	// budget (see GatewaySource).
	Gateways []GatewaySource
	// Ledger counts this process's one-api REST calls; with OneAPIRateLimit
	// above 0 the gate starts no call once the ledger holds that many within
	// oneapi.RateLimitWindow. A nil Ledger does not bound.
	Ledger          *oneapi.CallLedger
	OneAPIRateLimit int
	// Now overrides the clock in tests.
	Now func() time.Time
}

// UnmeteredOptions returns options for a validator that consults no external
// authority (the accept-all opt-out and test doubles): there is no upstream to
// protect, so every budget is effectively unlimited.
func UnmeteredOptions(name string) AuthenticatorOptions {
	const unlimited = 1 << 20
	return AuthenticatorOptions{
		Name:                      name,
		MaxConcurrent:             unlimited,
		UpstreamRatePerSecond:     unlimited,
		UpstreamBurst:             unlimited,
		PerSourceRatePerSecond:    unlimited,
		PerSourceBurst:            unlimited,
		RevalidationRatePerSecond: unlimited,
		RevalidationBurst:         unlimited,
	}
}

// Authenticator is the single credential gate shared by every entrypoint
// that derives a tenant from an API key. It composes three parts with one
// responsibility each:
//
//   - the verdict store (verdictStore) remembers what one-api answered per
//     key hash and derives trust from it (fresh, expired, rejected);
//   - the upstream gate (UpstreamGate) decides whether this server may start
//     another one-api call now, per source, globally and within one-api's
//     own per-IP window;
//   - Authenticate applies the policy: cached verdicts first, then one
//     coalesced upstream call, and a stale acceptance only when one-api
//     itself gave no verdict.
//
// Only its results satisfy FromContext. Balance serves one-api balance
// queries through the same store and gate.
type Authenticator struct {
	validator Validator
	opts      AuthenticatorOptions
	// authority is the canonical base URL of the validator's authority, or
	// "" for validators that consult none or whose endpoint names no base
	// (see BeginAuthorityCall).
	authority string
	verdicts  *verdictStore
	flights   singleflight.Group
	gate      *UpstreamGate
	staleLog  *LogSampler
}

// NewAuthenticator builds an Authenticator around an authoritative validator.
func NewAuthenticator(validator Validator, opts AuthenticatorOptions) (*Authenticator, error) {
	if validator == nil {
		return nil, errors.New("api key validator is required")
	}
	opts = normalizeOptions(opts)
	authority := ""
	if reporter, ok := validator.(authorityBaseURLReporter); ok {
		authority = canonicalBaseURL(reporter.AuthorityBaseURL())
	}
	return &Authenticator{
		validator: validator,
		opts:      opts,
		authority: authority,
		verdicts:  newVerdictStore(opts),
		gate:      newUpstreamGate(opts),
		staleLog:  NewLogSampler(time.Minute),
	}, nil
}

// normalizeOptions applies defaults to zero values and turns the negative
// "disabled" markers into an effective zero.
func normalizeOptions(opts AuthenticatorOptions) AuthenticatorOptions {
	defaults := DefaultValidationSettings()
	opts.PositiveTTL = durationOrDefault(opts.PositiveTTL, defaults.PositiveTTL)
	opts.NegativeTTL = durationOrDefault(opts.NegativeTTL, defaults.NegativeTTL)
	opts.Timeout = durationOrDefault(opts.Timeout, defaults.Timeout)
	switch {
	case opts.StaleGrace < 0:
		opts.StaleGrace = 0
	case opts.StaleGrace == 0:
		opts.StaleGrace = defaults.StaleGrace
	}
	switch {
	case opts.RevalidationRatePerSecond < 0:
		opts.RevalidationRatePerSecond = 0
	case opts.RevalidationRatePerSecond == 0:
		opts.RevalidationRatePerSecond = defaults.RevalidationRatePerSecond
	}
	opts.MaxEntries = intOrDefault(opts.MaxEntries, defaults.MaxEntries)
	opts.MaxConcurrent = intOrDefault(opts.MaxConcurrent, defaults.MaxConcurrent)
	opts.UpstreamBurst = intOrDefault(opts.UpstreamBurst, defaults.UpstreamBurst)
	opts.PerSourceBurst = intOrDefault(opts.PerSourceBurst, defaults.PerSourceBurst)
	opts.MaxSources = intOrDefault(opts.MaxSources, defaults.MaxSources)
	opts.RevalidationBurst = intOrDefault(opts.RevalidationBurst, defaults.RevalidationBurst)
	if opts.UpstreamRatePerSecond <= 0 {
		opts.UpstreamRatePerSecond = defaults.UpstreamRatePerSecond
	}
	if opts.PerSourceRatePerSecond <= 0 {
		opts.PerSourceRatePerSecond = defaults.PerSourceRatePerSecond
	}
	opts.OneAPIRateLimit = max(opts.OneAPIRateLimit, 0)
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Name == "" {
		opts.Name = "custom"
	}
	return opts
}

// ValidatorName reports which validator the authenticator consults.
func (a *Authenticator) ValidatorName() string {
	if a == nil {
		return ""
	}
	return a.opts.Name
}

// Validator returns the authoritative validator, for wiring checks.
func (a *Authenticator) Validator() Validator {
	if a == nil {
		return nil
	}
	return a.validator
}

// Authenticate parses the Authorization header and returns a verified context
// only when the credential authority accepts the key. It returns
// ErrMissingAuthorization for an absent header, ErrInvalidAuthorization for a
// malformed or oversized one, ErrCredentialRejected for a refused key, and
// ErrValidationUnavailable when no verdict can be obtained (with
// ErrUpstreamThrottled when this server's own budget refused the call). A nil
// receiver fails closed with ErrValidationUnavailable.
//
// A key one-api accepted before is served past its trust period only while
// one-api itself gives no verdict (errAuthorityNoVerdict) and within
// PositiveTTL+StaleGrace. Neither a refusal by this server's own budgets nor
// a caller that stopped waiting is evidence of an outage, so neither extends
// a key's life.
func (a *Authenticator) Authenticate(ctx context.Context, header string) (*Context, error) {
	candidate, err := a.parseCandidate(header)
	if err != nil {
		if IsUnavailable(err) {
			observeUnavailable(err)
		} else {
			observeDecision(decisionInvalid)
		}
		return nil, errors.WithStack(err)
	}

	now := a.opts.Now()
	trust := a.verdicts.trust(candidate.APIKeyHash, now)
	switch {
	case trust.level == trustFresh:
		observeDecision(decisionAcceptedCache)
		return markVerified(candidate), nil
	case trust.level == trustRejected:
		observeDecision(decisionRejectedCache)
		return nil, errors.WithStack(ErrCredentialRejected)
	case trust.known() && now.Before(trust.entry.retryAt):
		// one-api failed for this key moments ago; do not ask again yet.
		return a.staleOr(ctx, candidate, trust.entry, now,
			errors.Wrap(errors.Join(ErrValidationUnavailable, errAuthorityNoVerdict), "one-api was unavailable moments ago"))
	}

	class := callNewKey
	if trust.known() {
		class = callRevalidation
	}
	lookupErr := a.lookup(ctx, candidate, class)
	switch {
	case lookupErr == nil:
		observeDecision(decisionAcceptedAuthority)
		return markVerified(candidate), nil
	case isCredentialRejection(lookupErr):
		observeDecision(decisionRejectedAuthority)
		return nil, errors.WithStack(lookupErr)
	case !trust.known() || !isAuthorityFailure(lookupErr):
		observeUnavailable(lookupErr)
		return nil, errors.WithStack(lookupErr)
	}
	return a.staleOr(ctx, candidate, trust.entry, a.opts.Now(), lookupErr)
}

// isCredentialRejection reports a definitive refusal by the authority.
func isCredentialRejection(err error) bool {
	return errors.Is(err, ErrCredentialRejected)
}

// isAuthorityFailure reports an unavailable verdict that one-api itself
// caused, the only cause that may serve a stale acceptance. A refusal by this
// server's budgets (ErrUpstreamThrottled) and a waiter whose own context
// ended carry no errAuthorityNoVerdict.
func isAuthorityFailure(err error) bool {
	return errors.Is(err, errAuthorityNoVerdict) && !errors.Is(err, ErrUpstreamThrottled)
}

// staleOr serves a key one-api accepted within PositiveTTL+StaleGrace while
// one-api itself is unavailable, and otherwise returns cause, which must be
// an authority failure.
func (a *Authenticator) staleOr(ctx context.Context, candidate *Context, entry positiveEntry, now time.Time, cause error) (*Context, error) {
	if !a.verdicts.withinStaleGrace(entry, now) {
		observeUnavailable(cause)
		return nil, errors.WithStack(cause)
	}
	logger := gmw.GetLogger(ctx)
	if emit, suppressed := a.staleLog.Allow(now); emit {
		logger.Warn("one-api unavailable; serving recent api key validations",
			zap.Int64("suppressed_since_last", suppressed), zap.Error(cause))
	} else {
		logger.Debug("one-api unavailable; serving recent api key validation", zap.String("key_suffix", candidate.MaskedKey()))
	}
	observeDecision(decisionAcceptedStale)
	return markVerified(candidate), nil
}

// parseCandidate parses and bounds a bearer header (parseAuthorizationHeader
// refuses an oversized key before hashing it) ahead of any cache lookup or
// upstream call. A nil receiver fails closed.
func (a *Authenticator) parseCandidate(header string) (*Context, error) {
	candidate, err := parseAuthorizationHeader(header)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if a == nil {
		return nil, errors.Wrap(ErrValidationUnavailable, "no api key authenticator is installed")
	}
	return candidate, nil
}

// lookup coalesces concurrent validations of the same key into one upstream call.
func (a *Authenticator) lookup(ctx context.Context, candidate *Context, class callClass) error {
	_, err := a.coalesce(ctx, "validate:"+candidate.APIKeyHash, "wait for api key validation", func() (any, error) {
		return nil, a.validateUpstream(ctx, candidate, class)
	})
	return err
}

// coalesce runs fn once per flight key for all concurrent callers; a caller
// whose ctx ends first stops waiting with ErrValidationUnavailable (no
// authority failure) while the shared call completes for the others.
func (a *Authenticator) coalesce(ctx context.Context, flight, waiting string, fn func() (any, error)) (any, error) {
	results := a.flights.DoChan(flight, fn)
	select {
	case result := <-results:
		return result.Val, errors.WithStack(result.Err)
	case <-ctx.Done():
		return nil, errors.Wrap(errors.Join(ErrValidationUnavailable, ctx.Err()), waiting)
	}
}

// upstream runs one authority call for key through the gate's budgets,
// detached from the first caller's cancellation and bounded by Timeout, and
// records its verdict. Every result maps to nil, ErrCredentialRejected or
// ErrValidationUnavailable; only a call that started can carry
// errAuthorityNoVerdict. label names the call in metrics.
func (a *Authenticator) upstream(parent context.Context, key string, class callClass, label string, call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), a.opts.Timeout)
	defer cancel()
	release, err := a.gate.admit(ctx, class)
	if err != nil {
		return errors.WithStack(err)
	}
	defer release()

	err = call(ctx)
	observeAuthorityCall(label, err)
	if outcome := a.verdicts.record(key, err); outcome.rejectedKnown {
		authMetrics.knownKeyRejections.Inc()
	}
	switch {
	case err == nil, isCredentialRejection(err):
		return errors.WithStack(err)
	default:
		// The call started, so whatever went wrong happened at or on the way
		// to the authority: it gave no verdict.
		return errors.Wrap(errors.Join(ErrValidationUnavailable, errAuthorityNoVerdict, err), "call the api key authority")
	}
}

// validateUpstream asks the authority once, through the gate's budgets, and
// records the verdict. Shared by every coalesced caller, it is detached from
// the first caller's cancellation. Only a call that is about to start is
// charged: cache hits and coalesced waiters never reach the gate.
func (a *Authenticator) validateUpstream(parent context.Context, candidate *Context, class callClass) error {
	switch a.verdicts.trust(candidate.APIKeyHash, a.opts.Now()).level {
	case trustFresh:
		return nil
	case trustRejected:
		return errors.WithStack(ErrCredentialRejected)
	case trustUnknown, trustExpired:
	}
	label := callLabelNewKey
	if class == callRevalidation {
		label = callLabelRevalidation
	}
	return a.upstream(parent, candidate.APIKeyHash, class, label, func(ctx context.Context) error {
		return errors.WithStack(a.validator.ValidateAPIKey(ctx, candidate.APIKey))
	})
}

// markVerified flags a freshly parsed context as accepted by the authority.
func markVerified(candidate *Context) *Context {
	candidate.verified = true
	return candidate
}

// durationOrDefault keeps positive durations and falls back otherwise.
func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// intOrDefault keeps positive integers and falls back otherwise.
func intOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

var installed atomic.Pointer[Authenticator]

// Install sets the process-wide Authenticator used by every MCP entrypoint
// that does not receive one explicitly. Production wiring installs it at startup.
func Install(authenticator *Authenticator) {
	installed.Store(authenticator)
}

// Installed returns the process-wide Authenticator, or nil before Install.
func Installed() *Authenticator {
	return installed.Load()
}

// Authenticate validates a header with the installed Authenticator. Before
// Install it fails closed with ErrValidationUnavailable.
func Authenticate(ctx context.Context, header string) (*Context, error) {
	authenticated, err := Installed().Authenticate(ctx, header)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return authenticated, nil
}

// IsUnavailable reports whether err means no verdict could be obtained, which
// callers answer with a retryable 503 rather than a credential rejection.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrValidationUnavailable)
}
