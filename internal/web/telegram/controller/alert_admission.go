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

// authenticateAlert authenticates token for alertName and spends the alert's
// send. Both are canonical (service.NormalizeAlertName, NormalizePushToken). It
// is the GraphQL alert mutation's admission flow:
//
//  1. The credential directory refuses a name and token pair that matches no
//     alert type, with the refusal storage would give. That costs no storage
//     lookup and no budget, so an anonymous flood spends nothing.
//  2. A pair the directory knows, or cannot decide on because its copy failed or
//     is truncated, reserves a send (admitAlertAttempt) and is checked against
//     storage under the shared budget, as before the directory existed.
//  3. A matching token keeps the send; any other outcome returns the per-type send.
//
// It fails closed with errTelegramThrottleUnavailable when the throttle is not configured.
func (r *MutationResolver) authenticateAlert(ctx context.Context, alertName, token string) (*model.AlertTypes, error) {
	if telegramRatelimiter == nil {
		return nil, errors.WithStack(errTelegramThrottleUnavailable)
	}
	logger := gmw.GetLogger(ctx).Named("telegram_alert_credentials")
	verdict := credentialUnverifiable
	if r.credentials != nil {
		verdict = r.credentials.check(ctx, logger, alertName, token)
	}
	if verdict == credentialUnknown {
		if r.credentials.knowsName(alertName) {
			return nil, service.AlertTokenMismatchError(alertName)
		}
		return nil, service.AlertTypeNotFoundError(alertName)
	}

	admission, err := admitAlertAttempt(ctx, alertName)
	if err != nil {
		return nil, err
	}
	// Returns the reserved per-type send unless the credentials match below.
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

// verifyAlertCredential checks a credential against storage, which decides. If
// storage refuses a pair the directory knew, as after a token rotation, the pair
// is dropped and the next miss rereads, so the new token works on its first
// use. A refusal of a pair the directory could not decide on drops nothing, so
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
