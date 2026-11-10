package controller

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

const (
	authActionLogin    = "login"
	authActionRegister = "register"

	// accountGapLogInterval bounds how often the gate logs that an attacked
	// account could not be challenged on some entry point.
	accountGapLogInterval = time.Minute
)

// authFlow describes one authentication entry point for the risk gate.
type authFlow struct {
	// action names the flow in errors and logs.
	action string
	// account is the raw account the request names, or empty for flows that
	// name none (registration, GitHub OAuth start, passkey start).
	account string
	// acceptsToken reports whether the entry point can carry a Turnstile
	// token. The deprecated BlogLogin cannot.
	acceptsToken bool
}

// loginFlow returns the flow of a login entry point that can carry a token.
func loginFlow(account string) authFlow {
	return authFlow{action: authActionLogin, account: account, acceptsToken: true}
}

// registerFlow returns the flow of a registration entry point.
func registerFlow() authFlow {
	return authFlow{action: authActionRegister, acceptsToken: true}
}

// authAttempt is one request admitted by the adaptive-authentication gate. It
// holds the in-flight reservations made at admission until the caller reports
// the outcome: failed, succeeded, or finish for any other ending (an
// infrastructure error, a TOTP prompt, a flow without a credential check). The
// first report wins; later ones are no-ops, so callers can always defer finish.
// It is used by one request goroutine and is not safe for concurrent use.
type authAttempt struct {
	tracker     *authChallengeTracker
	subject     authRiskSubject
	identity    string
	reservation authReservation
	settled     bool
}

// failed records a credential failure for the attempt and releases it.
// identity is the store-resolved account identity (model.User.RiskIdentity or
// model.RejectedIdentity), or empty when the store did not reveal it.
func (a *authAttempt) failed(identity string) {
	if a == nil || a.settled {
		return
	}
	a.settled = true
	a.tracker.recordFailureFor(a.subject, a.identityKey(identity))
	a.tracker.release(a.reservation)
}

// succeeded records a successful authentication for the attempt and releases it.
func (a *authAttempt) succeeded(identity string) {
	if a == nil || a.settled {
		return
	}
	a.settled = true
	a.tracker.recordSuccessFor(a.subject, a.identityKey(identity))
	a.tracker.release(a.reservation)
}

// finish releases the attempt without recording an outcome.
func (a *authAttempt) finish() {
	if a == nil || a.settled {
		return
	}
	a.settled = true
	a.tracker.release(a.reservation)
}

// identityKey hashes a store identity, falling back to the identity the
// account spelling resolved to at admission.
func (a *authAttempt) identityKey(identity string) string {
	if key := a.tracker.identityKey(identity); key != "" {
		return key
	}
	return a.identity
}

// admitAuthRequest runs the adaptive-authentication gate for one request.
//
// The Turnstile pair is chosen from the request host (see resolveTurnstileSite),
// which can select among configured pairs but never disable the gate once a
// secret exists. A request is challenged when its client is high risk, or when
// its account is high risk and the client can actually present a challenge:
// the entry point accepts a token and the host's pair has a site key the page
// can render. Elsewhere the account signal is not applied (and the gap is
// logged at a bounded rate), so a third party can never lock an account out of
// an entry point where no challenge can be solved.
//
// Credential failures are recorded whether or not Turnstile is configured. The
// returned attempt must be settled by the caller; on error it is nil.
func admitAuthRequest(ctx context.Context, turnstileToken *string, flow authFlow) (*authAttempt, error) {
	tracker := getAuthChallengeTracker()
	attempt := &authAttempt{tracker: tracker, subject: resolveAuthRiskSubject(ctx, tracker, flow.account)}

	site := resolveTurnstileSite(resolveTurnstileRequestHost(ctx))
	if site.secret == "" {
		return attempt, nil
	}

	logger := gmw.GetLogger(ctx).Named("turnstile")
	// Record the attempt up front so request frequency is tracked even when the
	// client ultimately skips the challenge.
	tracker.recordAttempt(attempt.subject.client)

	token := ""
	if turnstileToken != nil {
		token = strings.TrimSpace(*turnstileToken)
	}
	if err := validateInputLength(turnstileTokenLengthLimit, token); err != nil {
		return nil, errors.Wrapf(model.ErrTurnstileFailed, "validate turnstile token length: %v", err)
	}

	accountEscalation := flow.acceptsToken && site.siteKey != ""
	decision := tracker.admit(attempt.subject, accountEscalation)
	attempt.identity = decision.identity
	if decision.accountGap {
		logAccountGap(logger, flow, attempt.subject, site)
	}

	// Requests whose client and account are both low risk proceed without a
	// challenge; any token they happen to send is ignored so a stale one
	// cannot block them. A client that just solved a challenge spends its
	// allowance here instead of solving another.
	if decision.admitted {
		attempt.reservation = decision.reservation
		return attempt, nil
	}

	if token == "" {
		// The account itself is never logged; only whether the flow named one.
		logger.Debug("turnstile challenge required",
			zap.String("action", flow.action),
			zap.String("client_key", attempt.subject.client),
			zap.Bool("account_scoped", attempt.subject.account != ""),
		)
		return nil, errors.Wrap(model.ErrTurnstileRequired, flow.action)
	}

	if err := verifyTurnstileToken(ctx, site.secret, token, resolveTurnstileClientIP(ctx)); err != nil {
		logger.Info("turnstile token rejected",
			zap.String("action", flow.action),
			zap.Error(err),
		)
		return nil, errors.Wrap(model.ErrTurnstileFailed, flow.action)
	}
	attempt.reservation = tracker.admitVerified(attempt.subject, attempt.identity)

	return attempt, nil
}

var (
	// accountGapLoggedAt is the UnixNano time the gap was last logged.
	accountGapLoggedAt atomic.Int64
	// accountGapSuppressed counts gaps not logged since then.
	accountGapSuppressed atomic.Int64
)

// logAccountGap logs, at most once per accountGapLogInterval, that an account
// above its failure threshold was not challenged because the entry point or the
// host cannot present a challenge.
func logAccountGap(logger glog.Logger, flow authFlow, subject authRiskSubject, site turnstileSite) {
	now := gutils.Clock.GetUTCNow().UnixNano()
	last := accountGapLoggedAt.Load()
	if now-last < int64(accountGapLogInterval) || !accountGapLoggedAt.CompareAndSwap(last, now) {
		accountGapSuppressed.Add(1)
		return
	}

	logger.Warn("account above its failure threshold was not challenged: this entry point cannot present a Turnstile challenge",
		zap.String("action", flow.action),
		zap.Bool("accepts_token", flow.acceptsToken),
		zap.Bool("site_key_configured", site.siteKey != ""),
		zap.String("client_key", subject.client),
		zap.Int64("suppressed_since_last", accountGapSuppressed.Swap(0)),
	)
}
