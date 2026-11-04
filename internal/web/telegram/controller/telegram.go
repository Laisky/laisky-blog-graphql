package telegram

import (
	"context"
	"reflect"
	"strconv"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// errTelegramServiceUnavailable is the base sentinel returned by resolver
// methods when the telegram service failed to initialize (e.g. tb.NewBot
// rejected the token or required databases were unreachable). Without this
// guard the resolver dereferences a nil service interface and panics for
// every request. Callers should wrap it with errors.WithStack so the stack
// reflects the actual return site rather than this var declaration.
var errTelegramServiceUnavailable = errors.New("telegram service not initialized")

// errTelegramThrottleUnavailable is returned when the alert rate limiter could
// not be built from configuration. Alerting is refused rather than sent
// unthrottled, and startup is not aborted for an optional subsystem.
var errTelegramThrottleUnavailable = errors.New("telegram alert throttle is not configured")

// errAlertThrottled is returned when the alert rate limiter denies a send. The
// GraphQL error classifier matches this text, so it must stay stable.
var errAlertThrottled = errors.New("deny by throttle")

// AlertTypeResolver alert type resolver
type AlertTypeResolver struct {
	svc service.Interface
}

// UserResolver user resolver
type UserResolver struct {
	svc service.Interface
}

// QueryResolver query resolver
type QueryResolver struct {
	svc service.Interface
}

// MutationResolver mutation resolver
type MutationResolver struct {
	svc service.Interface
	// credentials is the alert credential directory, nil without a service. The
	// Cloud Monitoring webhook built by CloudMonitoringWebhook shares it.
	credentials *alertCredentialDirectory
}

// NewQueryResolver new query resolver
func NewQueryResolver(svc service.Interface) QueryResolver {
	return QueryResolver{
		svc: presentService(svc),
	}
}

// NewMutationResolver new mutation resolver. A nil svc, including a nil
// pointer, makes every alert answer errTelegramServiceUnavailable.
func NewMutationResolver(svc service.Interface) *MutationResolver {
	r := &MutationResolver{svc: presentService(svc)}
	if r.svc != nil {
		r.credentials = newAlertCredentialDirectory(r.svc.LoadAlertCredentials, maxMirroredAlertCredentials, alertCredentialRefreshInterval)
	}
	return r
}

// presentService returns svc, or a nil interface when svc holds a nil pointer.
// A nil *service.Telegram, as when the service failed to start, would otherwise
// pass every nil check and panic on its first call.
func presentService(svc service.Interface) service.Interface {
	if svc == nil {
		return nil
	}
	if value := reflect.ValueOf(svc); value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	return svc
}

// Telegram telegram resolver
type Telegram struct {
	TelegramAlertTypeResolver   *AlertTypeResolver
	TelegramMonitorUserResolver *UserResolver
}

// NewTelegram builds the telegram GraphQL resolvers and installs the alert
// rate limiter for this process. A nil service is allowed: each resolver method
// then returns errTelegramServiceUnavailable instead of dereferencing it.
func NewTelegram(ctx context.Context, svc service.Interface) *Telegram {
	setupTelegramThrottle(ctx)
	svc = presentService(svc)
	return &Telegram{
		TelegramAlertTypeResolver:   &AlertTypeResolver{svc},
		TelegramMonitorUserResolver: &UserResolver{svc},
	}
}

// func isEnable() bool {
// 	return gconfig.Shared.Get("settings.telegram") != nil
// }

// func Initialize(ctx context.Context) {
// 	if !isEnable() {
// 		return
// 	}

// 	service.Initialize(ctx)

// 	setupTelegramThrottle(ctx)

// 	Instance = &Type{
// 		TelegramAlertTypeResolver: new(AlertTypeResolver),
// 		TelegramUserResolver:      new(UserResolver),
// 	}
// }

// TelegramMonitorUsers resolves the paged monitored-user list.
func (r *QueryResolver) TelegramMonitorUsers(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.MonitorUsers, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	page = pageOrDefault(page)
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadUsers(ctx, cfg)
}

// TelegramAlertTypes resolves the paged alert-type list.
func (r *QueryResolver) TelegramAlertTypes(ctx context.Context,
	page *models.Pagination,
	name string) ([]*model.AlertTypes, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	page = pageOrDefault(page)
	cfg := &dto.QueryCfg{
		Page: page.Page,
		Size: page.Size,
		Name: name,
	}
	return r.svc.LoadAlertTypes(ctx, cfg)
}

// pageOrDefault returns page, or the schema's default first page of 10 when a
// client passed page: null explicitly.
func pageOrDefault(page *models.Pagination) *models.Pagination {
	if page == nil {
		return &models.Pagination{Page: 0, Size: 10}
	}
	return page
}

// --------------------------
// telegram monitor resolver
// --------------------------
func (t *UserResolver) ID(ctx context.Context, obj *model.MonitorUsers) (string, error) {
	return obj.ID.Hex(), nil
}

// CreatedAt resolves the monitored user's creation timestamp.
func (t *UserResolver) CreatedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}

// ModifiedAt resolves the monitored user's last-modified timestamp.
func (t *UserResolver) ModifiedAt(ctx context.Context,
	obj *model.MonitorUsers,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}

// TelegramID resolves the monitored user's Telegram numeric ID as a string,
// because the value exceeds the range of a GraphQL Int.
func (t *UserResolver) TelegramID(ctx context.Context,
	obj *model.MonitorUsers,
) (string, error) {
	return strconv.FormatInt(int64(obj.UID), 10), nil
}

// SubAlerts resolves the alert types this monitored user subscribes to.
func (t *UserResolver) SubAlerts(ctx context.Context,
	obj *model.MonitorUsers,
) ([]*model.AlertTypes, error) {
	if t == nil || t.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	return t.svc.LoadAlertTypesByUser(ctx, obj)
}

// ID resolves the alert type's hex object identifier.
func (t *AlertTypeResolver) ID(ctx context.Context,
	obj *model.AlertTypes,
) (string, error) {
	return obj.ID.Hex(), nil
}

// CreatedAt resolves the alert type's creation timestamp.
func (t *AlertTypeResolver) CreatedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}

// ModifiedAt resolves the alert type's last-modified timestamp.
func (t *AlertTypeResolver) ModifiedAt(ctx context.Context,
	obj *model.AlertTypes,
) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.ModifiedAt), nil
}

// SubUsers resolves the monitored users subscribed to this alert type.
func (t *AlertTypeResolver) SubUsers(ctx context.Context,
	obj *model.AlertTypes,
) ([]*model.MonitorUsers, error) {
	if t == nil || t.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	return t.svc.LoadUsersByAlertType(ctx, obj)
}

// ============================
// mutations
// ============================

// TelegramMonitorAlert sends the complete alert through the existing authorized subscriber path.
// It preserves fail-closed throttling and token validation, and refuses credentials that match
// no alert type in memory before they spend any budget (see authenticateAlert); partial provider
// acceptance returns a visible error because retrying the mutation may duplicate accepted sends.
func (r *MutationResolver) TelegramMonitorAlert(ctx context.Context, typeArg, token, msg string) (*model.AlertTypes, error) {
	if r == nil || r.svc == nil {
		return nil, errors.WithStack(errTelegramServiceUnavailable)
	}
	complete, err := composeAlert(typeArg, msg)
	if err != nil {
		return nil, err
	}
	// Authentication and admission use the canonical name and token, so aliases
	// such as " name " share the budget of "name", and an input storage can
	// never match costs nothing. The errors keep the DAO's text.
	alertName, err := service.NormalizeAlertName(typeArg)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize alert type")
	}
	if token, err = service.NormalizePushToken(token); err != nil {
		return nil, errors.Wrap(err, "sanitize push token")
	}
	alert, err := r.authenticateAlert(ctx, alertName, token)
	if err != nil {
		return nil, err
	}
	return r.deliverAuthorizedAlert(ctx, alert, complete)
}

// composeAlert prefixes msg with the alert type header and returns an error when
// the complete text is over the payload limit, empty or not valid UTF-8.
func composeAlert(alertType, msg string) (string, error) {
	const separator = " >>>>>>>>>>>>>>>>>> \n"
	if len(alertType) > service.MaxAlertPayloadBytes-len(separator) || len(msg) > service.MaxAlertPayloadBytes-len(separator)-len(alertType) {
		return "", errors.WithStack(service.ErrAlertPayloadTooLarge)
	}
	complete := alertType + separator + msg
	if err := service.ValidateAlertPayload(complete); err != nil {
		return "", err
	}
	return complete, nil
}

// admitAlert spends one send from the alert type's and the total budget, for an
// alert whose credentials the caller already verified, such as the Cloud
// Monitoring webhook. A caller that has not authenticated yet must use
// admitAlertAttempt instead. It fails closed when the throttle is not configured.
func admitAlert(alertType string) error {
	if telegramRatelimiter == nil {
		return errors.WithStack(errTelegramThrottleUnavailable)
	}
	if !telegramRatelimiter.Allow(alertType) { //nolint:contextcheck // Rate admission does not need request context.
		return errors.WithStack(errAlertThrottled)
	}
	return nil
}

// deliverAuthorizedAlert sends complete once to each distinct subscriber of an
// alert that the caller already authenticated, and returns an *AlertFanoutError
// unless every send was acknowledged.
func (r *MutationResolver) deliverAuthorizedAlert(ctx context.Context, alert *model.AlertTypes, complete string) (*model.AlertTypes, error) {
	logger := gmw.GetLogger(ctx).Named("telegram_monitor_alert")
	users, err := r.svc.LoadUsersByAlertType(ctx, alert)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	recipients := make([]int, 0, len(users))
	seen := make(map[int]struct{}, len(users))
	for _, user := range users {
		if user == nil || user.UID == 0 {
			return alert, errors.New("telegram alert has an invalid subscriber")
		}
		if _, exists := seen[user.UID]; exists {
			continue
		}
		seen[user.UID] = struct{}{}
		recipients = append(recipients, user.UID)
	}
	if len(recipients) == 0 {
		return alert, errors.New("telegram alert has no subscribers; nothing was sent")
	}
	logger.Debug("send complete telegram alert", zap.Int("subscribers", len(recipients)), zap.Int("payload_bytes", len(complete)))
	outcome := &AlertFanoutError{Total: len(recipients)}
	for _, uid := range recipients {
		if ctx.Err() != nil {
			outcome.cause = ctx.Err()
			break
		}
		outcome.Attempted++
		err = r.svc.SendAlertToUser(ctx, uid, complete)
		if err == nil {
			outcome.Accepted++
		} else {
			var delivery *service.AlertDeliveryError
			if errors.As(err, &delivery) && !delivery.Unconfirmed {
				outcome.Rejected++
				outcome.RetryAfterSeconds = max(outcome.RetryAfterSeconds, delivery.RetryAfterSeconds)
			} else {
				outcome.Unconfirmed++
			}
		}
		if ctx.Err() != nil {
			outcome.cause = ctx.Err()
			break
		}
	}
	outcome.Unattempted = outcome.Total - outcome.Attempted
	if outcome.Accepted != outcome.Total {
		return alert, errors.WithStack(outcome)
	}
	return alert, nil
}
