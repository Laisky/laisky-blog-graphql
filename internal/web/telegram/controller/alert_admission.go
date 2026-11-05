package telegram

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"
	"golang.org/x/time/rate"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library/throttle"
)

// throttleSummaryInterval bounds the throttle summary to one log line per
// interval, so a flood leaves a steady signal without one entry per request.
const throttleSummaryInterval = time.Minute

// throttleSummaryLog paces the throttle summary across requests.
var throttleSummaryLog = rate.Sometimes{Interval: throttleSummaryInterval}

// authenticateAlert verifies canonical credentials and spends a delivery send.
// Known pairs reserve before storage; all other pairs use a separately bounded
// lookup before admission, so guessing cannot spend delivery capacity. A miss
// may represent a remote rotation, so a full lookup budget is retryable.
func (r *MutationResolver) authenticateAlert(ctx context.Context, alertName, token string) (*model.AlertTypes, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if telegramRatelimiter == nil {
		return nil, errors.WithStack(errTelegramThrottleUnavailable)
	}
	logger := gmw.GetLogger(ctx).Named("telegram_alert_credentials")
	verdict := credentialUnverifiable
	if r.credentials != nil {
		verdict = r.credentials.check(ctx, logger, alertName, token)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if verdict != credentialKnown {
		alert, err := r.verifyUnknownCredential(ctx, verdict, alertName, token)
		if err != nil {
			return nil, err
		}
		admission, err := admitAlertAttempt(ctx, alert.Name)
		if err != nil {
			return nil, err
		}
		defer admission.Cancel()
		if err = authorizeAlert(ctx, admission, alert.Name); err != nil {
			return nil, err
		}
		return alert, nil
	}

	admission, err := admitAlertAttempt(ctx, alertName)
	if err != nil {
		return nil, err
	}
	// Returns the safely refundable per-type capacity unless credentials match.
	defer admission.Cancel()
	alert, err := r.verifyAlertCredential(ctx, verdict, alertName, token)
	if err != nil {
		return nil, err
	}
	if err = authorizeAlert(ctx, admission, alert.Name); err != nil {
		return nil, err
	}
	return alert, nil
}

// verifyUnknownCredential bounds authoritative lookups independently of the
// delivery budgets. A miss can be a token rotated by another process, so a
// rate-limited miss is retryable rather than an authentication refusal.
func (r *MutationResolver) verifyUnknownCredential(ctx context.Context, verdict credentialVerdict, alertName, token string) (*model.AlertTypes, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if r.credentials == nil {
		return nil, errors.WithStack(errTelegramThrottleUnavailable)
	}
	select {
	case r.credentials.fallbackSlots <- struct{}{}:
		defer func() { <-r.credentials.fallbackSlots }()
	default:
		return nil, errors.WithStack(errAlertThrottled)
	}
	if !r.credentials.fallback.Allow() {
		return nil, errors.WithStack(errAlertThrottled)
	}
	return r.verifyAlertCredential(ctx, verdict, alertName, token)
}

// verifyAlertCredential checks a credential against storage, which decides. If
// storage refuses a pair the directory knew, as after a token rotation, the pair
// is dropped and the next miss rereads. A new token may also use the bounded
// fallback, which can be saturated. A refusal of an undecided pair drops nothing, so
// it cannot force rereads. Both alert entrypoints use it.
func (r *MutationResolver) verifyAlertCredential(ctx context.Context, verdict credentialVerdict, alertType, token string) (*model.AlertTypes, error) {
	alert, err := r.svc.ValidateTokenForAlertType(ctx, token, alertType)
	if err == nil && alert == nil {
		err = errors.New("telegram alert authorization returned no alert")
	}
	if err != nil {
		if verdict == credentialKnown && r.credentials != nil && errors.Is(err, service.ErrAlertUnauthorized) {
			r.credentials.forget(alertType, token)
		}
		return nil, errors.WithStack(err)
	}
	return alert, nil
}

// admitAlertAttempt reserves one send for alertName, the canonical requested
// alert type, before its credential lookup. The caller must settle the returned
// admission with authorizeAlert or Cancel. It fails closed when the throttle is
// not configured.
func admitAlertAttempt(ctx context.Context, alertName string) (*throttle.Admission, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	if telegramRatelimiter == nil {
		return nil, errors.WithStack(errTelegramThrottleUnavailable)
	}
	admission, err := telegramRatelimiter.Admit(alertName)
	if err != nil {
		return nil, throttleRefusal(ctx, alertName, err)
	}
	return admission, nil
}

// authorizeAlert settles admission once the credentials of alertName, the stored
// name of the alert, matched, so its reserved send stays spent.
func authorizeAlert(ctx context.Context, admission *throttle.Admission, alertName string) error {
	if err := admission.Authorize(alertName); err != nil {
		return throttleRefusal(ctx, alertName, err)
	}
	return nil
}

// throttleRefusal turns a budget refusal for alertName into errAlertThrottled,
// whose text clients and the GraphQL error classifier match. It records which
// budget refused at debug level and, at most once per throttleSummaryInterval,
// warns with the throttle's counters. Any other error is returned wrapped.
func throttleRefusal(ctx context.Context, alertName string, cause error) error {
	var budget string
	switch {
	case errors.Is(cause, throttle.ErrTotalBudgetExhausted):
		budget = "total"
	case errors.Is(cause, throttle.ErrTitleBudgetExhausted):
		budget = "alert_type"
	default:
		return errors.Wrap(cause, "admit telegram alert")
	}

	logger := gmw.GetLogger(ctx).Named("telegram_alert_throttle")
	logger.Debug("telegram alert throttled", zap.String("budget", budget), zap.String("alert_type", alertName))
	throttleSummaryLog.Do(func() {
		stats := telegramRatelimiter.Stats()
		logger.Warn("telegram alerts are being throttled",
			zap.Uint64("total_refusals", stats.TotalRefusals),
			zap.Uint64("alert_type_refusals", stats.TitleRefusals),
			zap.Uint64("admitted", stats.Admitted),
			zap.Uint64("canceled", stats.Canceled),
			zap.Int("retained_alert_types", stats.RetainedTitles),
		)
	})
	return errors.WithStack(errAlertThrottled)
}
