package auth

import (
	"context"
	"sync/atomic"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"golang.org/x/sync/singleflight"
)

// MaxAPIKeyLength bounds a presented API key before hashing or any upstream call.
// one-api keys, including a channel suffix, are far shorter.
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

// Timing constants of the key caches.
const (
	// knownKeyRetention keeps a key "known" (eligible for the reserved
	// revalidation budget) long after its trust period, independently of the
	// stale grace window.
	knownKeyRetention = 24 * time.Hour
	// unavailableRetry spaces upstream retries of one known key after one-api
	// failed for it, so an outage cannot spend the reserve on a single key.
	unavailableRetry = 15 * time.Second
	// balanceTTL is how long a balance answer is reused for the same key.
	balanceTTL = 30 * time.Second
)

// positiveEntry records when one-api last accepted a key.
type positiveEntry struct {
	validatedAt time.Time
	// retryAt delays the next upstream call after one-api failed for the key.
	retryAt time.Time
}

// Authenticator is the single credential gate shared by every MCP entrypoint:
// it parses a bearer, validates it with an authoritative Validator through the
// UpstreamGate, and caches the verdict by key hash. Only its results satisfy
// FromContext. It also serves one-api balance queries through the same gate.
type Authenticator struct {
	validator Validator
	opts      AuthenticatorOptions
	positive  *ttlCache[positiveEntry]
	negative  *ttlCache[struct{}]
	balances  *ttlCache[Balance]
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
	return &Authenticator{
		validator: validator,
		opts:      opts,
		positive:  newTTLCache[positiveEntry](opts.MaxEntries),
		negative:  newTTLCache[struct{}](opts.MaxEntries),
		balances:  newTTLCache[Balance](opts.MaxEntries),
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
// malformed one, ErrCredentialRejected for a refused key, and
// ErrValidationUnavailable when no verdict can be obtained (with
// ErrUpstreamThrottled when this server's own budget refused the call). A nil
// receiver fails closed with ErrValidationUnavailable.
func (a *Authenticator) Authenticate(ctx context.Context, header string) (*Context, error) {
	candidate, err := parseAuthorizationHeader(header)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if len(candidate.APIKey) > MaxAPIKeyLength {
		return nil, errors.Wrap(ErrInvalidAuthorization, "api key exceeds the maximum length")
	}
	if a == nil {
		return nil, errors.Wrap(ErrValidationUnavailable, "no api key authenticator is installed")
	}

	now := a.opts.Now()
	entry, known := a.positive.get(candidate.APIKeyHash, now)
	if known && now.Sub(entry.validatedAt) < a.opts.PositiveTTL {
		return markVerified(candidate), nil
	}
	if _, rejected := a.negative.get(candidate.APIKeyHash, now); rejected {
		return nil, errors.WithStack(ErrCredentialRejected)
	}
	if known && now.Before(entry.retryAt) {
		// one-api failed for this key moments ago; do not ask again yet.
		return a.staleOr(ctx, candidate, entry, now, errors.Wrap(ErrValidationUnavailable, "one-api was unavailable moments ago"))
	}

	class := callNewKey
	if known {
		class = callRevalidation
	}
	lookupErr := a.lookup(ctx, candidate, class)
	switch {
	case lookupErr == nil:
		return markVerified(candidate), nil
	case errors.Is(lookupErr, ErrCredentialRejected), errors.Is(lookupErr, ErrUpstreamThrottled), !known:
		// A refusal by this server's own budget never grants stale-if-error.
		return nil, errors.WithStack(lookupErr)
	}
	return a.staleOr(ctx, candidate, entry, a.opts.Now(), lookupErr)
}

// staleOr serves a key one-api accepted within PositiveTTL+StaleGrace while
// one-api itself is unavailable, and otherwise returns cause.
func (a *Authenticator) staleOr(ctx context.Context, candidate *Context, entry positiveEntry, now time.Time, cause error) (*Context, error) {
	if a.opts.StaleGrace <= 0 || now.Sub(entry.validatedAt) >= a.opts.PositiveTTL+a.opts.StaleGrace {
		return nil, errors.WithStack(cause)
	}
	logger := gmw.GetLogger(ctx)
	if emit, suppressed := a.staleLog.Allow(now); emit {
		logger.Warn("one-api unavailable; serving recent api key validations",
			zap.Int64("suppressed_since_last", suppressed), zap.Error(cause))
	} else {
		logger.Debug("one-api unavailable; serving recent api key validation", zap.String("key_suffix", candidate.MaskedKey()))
	}
	return markVerified(candidate), nil
}

// lookup coalesces concurrent validations of the same key into one upstream call.
func (a *Authenticator) lookup(ctx context.Context, candidate *Context, class callClass) error {
	results := a.flights.DoChan("validate:"+candidate.APIKeyHash, func() (any, error) {
		return nil, a.validateUpstream(ctx, candidate, class)
	})
	select {
	case result := <-results:
		return result.Err
	case <-ctx.Done():
		return errors.Wrap(errors.Join(ErrValidationUnavailable, ctx.Err()), "wait for api key validation")
	}
}

// validateUpstream asks the authority once, through the gate's budgets, and
// records the verdict. Shared by every coalesced caller, it is detached from
// the first caller's cancellation. Only a call that is about to start is
// charged: cache hits and coalesced waiters never reach the gate.
func (a *Authenticator) validateUpstream(parent context.Context, candidate *Context, class callClass) error {
	key := candidate.APIKeyHash
	now := a.opts.Now()
	if entry, ok := a.positive.get(key, now); ok && now.Sub(entry.validatedAt) < a.opts.PositiveTTL {
		return nil
	}
	if _, rejected := a.negative.get(key, now); rejected {
		return errors.WithStack(ErrCredentialRejected)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), a.opts.Timeout)
	defer cancel()
	release, err := a.gate.admit(ctx, class)
	if err != nil {
		return errors.WithStack(err)
	}
	defer release()

	err = a.validator.ValidateAPIKey(ctx, candidate.APIKey)
	a.recordVerdict(key, err)
	switch {
	case err == nil, errors.Is(err, ErrCredentialRejected), errors.Is(err, ErrValidationUnavailable):
		return errors.WithStack(err)
	default:
		return errors.Wrap(errors.Join(ErrValidationUnavailable, err), "validate api key")
	}
}

// recordVerdict stores the outcome of one upstream call for key.
func (a *Authenticator) recordVerdict(key string, err error) {
	now := a.opts.Now()
	switch {
	case err == nil:
		a.negative.remove(key)
		a.positive.put(key, positiveEntry{validatedAt: now}, a.positiveExpiry(now))
	case errors.Is(err, ErrCredentialRejected):
		a.positive.remove(key)
		a.balances.remove(key)
		a.negative.put(key, struct{}{}, now.Add(a.opts.NegativeTTL))
	default:
		if entry, ok := a.positive.get(key, now); ok {
			entry.retryAt = now.Add(unavailableRetry)
			a.positive.put(key, entry, a.positiveExpiry(entry.validatedAt))
		}
	}
}

// positiveExpiry is when an acceptance at validatedAt stops being "known".
func (a *Authenticator) positiveExpiry(validatedAt time.Time) time.Time {
	return validatedAt.Add(a.opts.PositiveTTL + max(a.opts.StaleGrace, knownKeyRetention))
}

// Balance returns the one-api quota of apiKey and validates the key in the
// same call. Answers are reused for a short time per key hash, and every
// upstream call is charged to the caller's per-source and the global budget.
// It fails like Authenticate: ErrCredentialRejected or ErrValidationUnavailable.
func (a *Authenticator) Balance(ctx context.Context, apiKey string) (Balance, error) {
	candidate, err := parseAuthorizationHeader("Bearer " + apiKey)
	if err != nil {
		return Balance{}, errors.WithStack(err)
	}
	if len(candidate.APIKey) > MaxAPIKeyLength {
		return Balance{}, errors.Wrap(ErrInvalidAuthorization, "api key exceeds the maximum length")
	}
	if a == nil {
		return Balance{}, errors.Wrap(ErrValidationUnavailable, "no api key authenticator is installed")
	}
	fetcher, ok := a.validator.(BalanceValidator)
	if !ok {
		return Balance{}, errors.Wrap(ErrValidationUnavailable, "the configured validator reports no balance")
	}

	key := candidate.APIKeyHash
	now := a.opts.Now()
	if _, rejected := a.negative.get(key, now); rejected {
		return Balance{}, errors.WithStack(ErrCredentialRejected)
	}
	if balance, cached := a.balances.get(key, now); cached {
		return balance, nil
	}

	results := a.flights.DoChan("balance:"+key, func() (any, error) {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.opts.Timeout)
		defer cancel()
		release, err := a.gate.admit(callCtx, callNewKey)
		if err != nil {
			return Balance{}, errors.WithStack(err)
		}
		defer release()

		balance, err := fetcher.FetchBalance(callCtx, candidate.APIKey)
		a.recordVerdict(key, err)
		if err != nil {
			return Balance{}, errors.WithStack(err)
		}
		a.balances.put(key, balance, a.opts.Now().Add(balanceTTL))
		return balance, nil
	})
	select {
	case result := <-results:
		if result.Err != nil {
			return Balance{}, errors.WithStack(result.Err)
		}
		balance, _ := result.Val.(Balance)
		return balance, nil
	case <-ctx.Done():
		return Balance{}, errors.Wrap(errors.Join(ErrValidationUnavailable, ctx.Err()), "wait for one-api balance")
	}
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
