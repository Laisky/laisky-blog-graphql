package auth

import (
	"context"
	"sync/atomic"
	"time"

	errors "github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
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
// Zero values take the defaults of DefaultValidationSettings.
type AuthenticatorOptions struct {
	// Name labels the validator in logs and wiring checks, e.g. "oneapi".
	Name string
	// PositiveTTL is how long an accepted key is trusted without asking again.
	PositiveTTL time.Duration
	// NegativeTTL is how long a rejected key is refused without asking again.
	NegativeTTL time.Duration
	// StaleGrace extends a previous acceptance only while the authority is unavailable.
	StaleGrace time.Duration
	// Timeout bounds one upstream validation.
	Timeout time.Duration
	// MaxEntries bounds each of the positive and negative caches.
	MaxEntries int
	// MaxConcurrent bounds simultaneous upstream validations.
	MaxConcurrent int
	// UpstreamRatePerSecond and UpstreamBurst bound upstream validations of uncached keys.
	UpstreamRatePerSecond float64
	UpstreamBurst         int
	// Now overrides the clock in tests.
	Now func() time.Time
}

// positiveEntry records when a key was last accepted by the authority.
type positiveEntry struct {
	validatedAt time.Time
}

// Authenticator is the single credential gate shared by every MCP entrypoint:
// it parses a bearer, validates it with an authoritative Validator, and caches
// the verdict by key hash. Only its results satisfy FromContext.
type Authenticator struct {
	validator Validator
	opts      AuthenticatorOptions
	positive  *ttlCache[positiveEntry]
	negative  *ttlCache[struct{}]
	flights   singleflight.Group
	slots     chan struct{}
	limiter   *rate.Limiter
}

// NewAuthenticator builds an Authenticator around an authoritative validator.
func NewAuthenticator(validator Validator, opts AuthenticatorOptions) (*Authenticator, error) {
	if validator == nil {
		return nil, errors.New("api key validator is required")
	}
	defaults := DefaultValidationSettings()
	opts.PositiveTTL = durationOrDefault(opts.PositiveTTL, defaults.PositiveTTL)
	opts.NegativeTTL = durationOrDefault(opts.NegativeTTL, defaults.NegativeTTL)
	opts.StaleGrace = durationOrDefault(opts.StaleGrace, defaults.StaleGrace)
	opts.Timeout = durationOrDefault(opts.Timeout, defaults.Timeout)
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaults.MaxEntries
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaults.MaxConcurrent
	}
	if opts.UpstreamRatePerSecond <= 0 {
		opts.UpstreamRatePerSecond = defaults.UpstreamRatePerSecond
	}
	if opts.UpstreamBurst <= 0 {
		opts.UpstreamBurst = defaults.UpstreamBurst
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Name == "" {
		opts.Name = "custom"
	}

	return &Authenticator{
		validator: validator,
		opts:      opts,
		positive:  newTTLCache[positiveEntry](opts.MaxEntries),
		negative:  newTTLCache[struct{}](opts.MaxEntries),
		slots:     make(chan struct{}, opts.MaxConcurrent),
		limiter:   rate.NewLimiter(rate.Limit(opts.UpstreamRatePerSecond), opts.UpstreamBurst),
	}, nil
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
// ErrValidationUnavailable when no verdict can be obtained; a nil receiver
// fails closed with ErrValidationUnavailable.
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
	if entry, ok := a.positive.get(candidate.APIKeyHash, now); ok && now.Sub(entry.validatedAt) < a.opts.PositiveTTL {
		return markVerified(candidate), nil
	}
	if _, rejected := a.negative.get(candidate.APIKeyHash, now); rejected {
		return nil, errors.WithStack(ErrCredentialRejected)
	}

	lookupErr := a.lookup(ctx, candidate)
	switch {
	case lookupErr == nil:
		return markVerified(candidate), nil
	case errors.Is(lookupErr, ErrCredentialRejected):
		return nil, errors.WithStack(lookupErr)
	}

	// Stale-if-error: only a key the authority accepted within the grace window
	// survives an outage; a never-seen key fails closed.
	if _, ok := a.positive.get(candidate.APIKeyHash, a.opts.Now()); ok {
		gmw.GetLogger(ctx).Warn("api key authority unavailable; serving recent validation",
			zap.String("key_suffix", candidate.MaskedKey()),
			zap.Error(lookupErr),
		)
		return markVerified(candidate), nil
	}
	return nil, errors.WithStack(lookupErr)
}

// lookup coalesces concurrent validations of the same key into one upstream call.
func (a *Authenticator) lookup(ctx context.Context, candidate *Context) error {
	results := a.flights.DoChan(candidate.APIKeyHash, func() (any, error) {
		return nil, a.validateUpstream(ctx, candidate)
	})
	select {
	case result := <-results:
		return result.Err
	case <-ctx.Done():
		return errors.Wrap(errors.Join(ErrValidationUnavailable, ctx.Err()), "wait for api key validation")
	}
}

// validateUpstream asks the authority once, within the rate, concurrency and
// time budgets, and records the verdict. Shared by every coalesced caller, it
// is detached from the first caller's cancellation.
func (a *Authenticator) validateUpstream(parent context.Context, candidate *Context) error {
	logger := gmw.GetLogger(parent)
	key := candidate.APIKeyHash
	now := a.opts.Now()
	if entry, ok := a.positive.get(key, now); ok && now.Sub(entry.validatedAt) < a.opts.PositiveTTL {
		return nil
	}
	if _, rejected := a.negative.get(key, now); rejected {
		return errors.WithStack(ErrCredentialRejected)
	}
	if !a.limiter.Allow() {
		return errors.Wrap(ErrValidationUnavailable, "api key validation rate exceeded")
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), a.opts.Timeout)
	defer cancel()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return errors.Wrap(errors.Join(ErrValidationUnavailable, ctx.Err()), "wait for an api key validation slot")
	}

	err := a.validator.ValidateAPIKey(ctx, candidate.APIKey)
	now = a.opts.Now()
	switch {
	case err == nil:
		a.negative.remove(key)
		a.positive.put(key, positiveEntry{validatedAt: now}, now.Add(a.opts.PositiveTTL+a.opts.StaleGrace))
		logger.Debug("api key validated", zap.String("key_suffix", candidate.MaskedKey()))
		return nil
	case errors.Is(err, ErrCredentialRejected):
		a.positive.remove(key)
		a.negative.put(key, struct{}{}, now.Add(a.opts.NegativeTTL))
		return errors.WithStack(err)
	case errors.Is(err, ErrValidationUnavailable):
		return errors.WithStack(err)
	default:
		return errors.Wrap(errors.Join(ErrValidationUnavailable, err), "validate api key")
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
